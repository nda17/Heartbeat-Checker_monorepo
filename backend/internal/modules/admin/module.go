package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	accountcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
	billingcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors"
	monitorcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
)

var ErrInvalid = errors.New("invalid admin request")
var ErrNotFound = errors.New("admin target not found")
var ErrConflict = errors.New("admin version conflict")
var ErrForbidden = errors.New("admin mutation forbidden")

type Module struct {
	pool     *pgxpool.Pool
	accounts *accounts.Module
	auth     *auth.Module
	billing  *billing.Module
	monitors *monitors.Module
}

func New(pool *pgxpool.Pool, a *accounts.Module, authModule *auth.Module, b *billing.Module, m *monitors.Module) *Module {
	return &Module{pool: pool, accounts: a, auth: authModule, billing: b, monitors: m}
}

type Account struct {
	accountcontracts.Account
	Email string `json:"email"`
}

type AccountDetail struct {
	Account       Account                      `json:"account"`
	Entitlement   billingcontracts.Entitlement `json:"entitlement"`
	SessionsCount int                          `json:"sessionsCount"`
}

type AuditEvent struct {
	ID              string          `json:"id"`
	OperationID     *string         `json:"operationId"`
	ActorAccountID  *string         `json:"actorAccountId"`
	ActorSessionID  *string         `json:"actorSessionId"`
	TargetAccountID *string         `json:"targetAccountId"`
	TargetID        *string         `json:"targetId"`
	Action          string          `json:"action"`
	Reason          string          `json:"reason"`
	BeforeState     json.RawMessage `json:"beforeState"`
	AfterState      json.RawMessage `json:"afterState"`
	Metadata        json.RawMessage `json:"metadata"`
	CreatedAt       time.Time       `json:"createdAt"`
}

func validID(id string) bool {
	var value pgtype.UUID
	return len(id) == 36 && value.Scan(id) == nil && value.Valid && value.String() == id
}
func validReason(reason string) bool { return strings.TrimSpace(reason) != "" && len(reason) <= 500 }

func validAccountPatch(in AccountPatch) bool {
	return (in.Role == nil || *in.Role == "user" || *in.Role == "admin") &&
		(in.Status == nil || *in.Status == "active" || *in.Status == "blocked")
}

func (m *Module) accountWithEmail(ctx context.Context, tx pgx.Tx, a accountcontracts.Account) (Account, error) {
	email, err := m.auth.AdminEmail(ctx, tx, a.ID)
	if err != nil {
		return Account{}, err
	}
	return Account{Account: a, Email: email}, nil
}

func (m *Module) ListAccounts(ctx context.Context, q, cursor string, limit int) ([]Account, string, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || len(q) > 254 || len(cursor) > 256 {
		return nil, "", ErrInvalid
	}
	var beforeTime *time.Time
	var beforeID *string
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, "", ErrInvalid
		}
		parts := strings.Split(string(decoded), "|")
		if len(parts) != 2 || !validID(parts[1]) {
			return nil, "", ErrInvalid
		}
		parsed, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil {
			return nil, "", ErrInvalid
		}
		beforeTime, beforeID = &parsed, &parts[1]
	}
	rows, err := m.pool.Query(ctx, `SELECT a.id::text,a.name,a.role,a.status,a.version,a.created_at,c.email
		FROM accounts.accounts a JOIN auth.credentials c ON c.account_id=a.id
		WHERE ($1='' OR a.name ILIKE '%'||$1||'%' OR c.email ILIKE '%'||$1||'%')
		AND ($2::timestamptz IS NULL OR (a.created_at,a.id)<($2::timestamptz,$3::uuid))
		ORDER BY a.created_at DESC,a.id DESC LIMIT $4`, strings.TrimSpace(q), beforeTime, beforeID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := make([]Account, 0, limit+1)
	for rows.Next() {
		var item Account
		if err := rows.Scan(&item.ID, &item.Name, &item.Role, &item.Status, &item.Version, &item.CreatedAt, &item.Email); err != nil {
			return nil, "", err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID))
	}
	return items, next, nil
}

func (m *Module) GetAccount(ctx context.Context, accountID string) (AccountDetail, error) {
	if !validID(accountID) {
		return AccountDetail{}, ErrNotFound
	}
	a, err := m.accounts.Get(ctx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountDetail{}, ErrNotFound
	}
	if err != nil {
		return AccountDetail{}, err
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return AccountDetail{}, err
	}
	defer tx.Rollback(ctx)
	account, err := m.accountWithEmail(ctx, tx, a)
	if err != nil {
		return AccountDetail{}, err
	}
	count, err := m.auth.AdminSessionCount(ctx, tx, a.ID)
	if err != nil {
		return AccountDetail{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AccountDetail{}, err
	}
	e, err := m.monitors.Entitlement(ctx, accountID)
	return AccountDetail{Account: account, Entitlement: e, SessionsCount: count}, err
}

type mutation struct {
	tx          pgx.Tx
	target      accountcontracts.Account
	entitlement billingcontracts.Entitlement
}

// Every administrative mutation rechecks actor status, session token and MFA
// while holding one transaction. Lock order matches monitor billing acceptance.
func (m *Module) beginMutation(ctx context.Context, p authcontracts.Principal, targetID string) (*mutation, error) {
	if !validID(targetID) || !validID(p.AccountID) {
		return nil, ErrNotFound
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(73421,1)`); err != nil {
		return nil, err
	}
	ids := []string{p.AccountID}
	if targetID != p.AccountID {
		ids = append(ids, targetID)
	}
	sort.Strings(ids)
	entitlements := make(map[string]billingcontracts.Entitlement, len(ids))
	for _, id := range ids {
		e, err := m.billing.Lock(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		entitlements[id] = e
	}
	locked := make(map[string]accountcontracts.Account, len(ids))
	for _, id := range ids {
		a, err := m.accounts.LockAdminAccount(ctx, tx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		locked[id] = a
	}
	actor := locked[p.AccountID]
	if actor.Role != "admin" || actor.Status != "active" {
		return nil, ErrForbidden
	}
	if locked[targetID].Status == "deleted" {
		return nil, ErrNotFound
	}
	if err := m.auth.VerifyAdminTx(ctx, tx, p, true); err != nil {
		if errors.Is(err, auth.ErrAdminVerificationDenied) {
			return nil, ErrForbidden
		}
		return nil, err
	}
	targetEntitlement := entitlements[targetID]
	if locked[targetID].Status != "active" {
		targetEntitlement.Quota = 0
	}
	rollback = false
	return &mutation{tx: tx, target: locked[targetID], entitlement: targetEntitlement}, nil
}

func (m *Module) audit(ctx context.Context, tx pgx.Tx, p authcontracts.Principal, accountID, targetID, action, reason string, before, after any) error {
	b, err := json.Marshal(before)
	if err != nil {
		return err
	}
	a, err := json.Marshal(after)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO admin.audit_events(actor_account_id,actor_session_id,target_account_id,target_id,action,reason,before_state,after_state)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.AccountID, p.SessionID, accountID, targetID, action, reason, b, a)
	return err
}

type AccountPatch struct {
	Version int64   `json:"version"`
	Name    *string `json:"name"`
	Role    *string `json:"role"`
	Status  *string `json:"status"`
	Reason  string  `json:"reason"`
}

func (m *Module) PatchAccount(ctx context.Context, p authcontracts.Principal, id string, in AccountPatch) (Account, error) {
	if !validReason(in.Reason) || !validAccountPatch(in) || in.Version < 1 || (in.Name == nil && in.Role == nil && in.Status == nil) {
		return Account{}, ErrInvalid
	}
	mut, err := m.beginMutation(ctx, p, id)
	if err != nil {
		return Account{}, err
	}
	defer mut.tx.Rollback(ctx)
	if id == p.AccountID && ((in.Role != nil && *in.Role != "admin") || (in.Status != nil && *in.Status != "active")) {
		return Account{}, ErrForbidden
	}
	before := map[string]any{"name": mut.target.Name, "role": mut.target.Role, "status": mut.target.Status, "version": mut.target.Version}
	a, changedAccess, err := m.accounts.AdminUpdate(ctx, mut.tx, mut.target, in.Version, in.Name, in.Role, in.Status)
	if errors.Is(err, accounts.ErrAdminConflict) {
		return Account{}, ErrConflict
	}
	if errors.Is(err, accounts.ErrLastAdmin) {
		return Account{}, ErrForbidden
	}
	if err != nil {
		return Account{}, err
	}
	if changedAccess {
		if err := m.auth.RevokeAccountSessionsTx(ctx, mut.tx, id); err != nil {
			return Account{}, err
		}
	}
	if err := m.audit(ctx, mut.tx, p, id, id, "account.updated", in.Reason, before, map[string]any{"name": a.Name, "role": a.Role, "status": a.Status, "version": a.Version}); err != nil {
		return Account{}, err
	}
	result, err := m.accountWithEmail(ctx, mut.tx, a)
	if err != nil {
		return Account{}, err
	}
	return result, mut.tx.Commit(ctx)
}

func (m *Module) RevokeSessions(ctx context.Context, p authcontracts.Principal, id, reason string) error {
	if !validReason(reason) {
		return ErrInvalid
	}
	mut, err := m.beginMutation(ctx, p, id)
	if err != nil {
		return err
	}
	defer mut.tx.Rollback(ctx)
	if err := m.auth.RevokeAccountSessionsTx(ctx, mut.tx, id); err != nil {
		return err
	}
	if err := m.audit(ctx, mut.tx, p, id, id, "account.sessions_revoked", reason, map[string]any{}, map[string]any{}); err != nil {
		return err
	}
	return mut.tx.Commit(ctx)
}

type GrantInput struct {
	Version    int64     `json:"version"`
	Quota      int       `json:"quota"`
	ValidUntil time.Time `json:"validUntil"`
	Reason     string    `json:"reason"`
}

func (m *Module) Grant(ctx context.Context, p authcontracts.Principal, id string, in GrantInput) (billingcontracts.Entitlement, error) {
	if !validReason(in.Reason) || in.Version < 0 {
		return billingcontracts.Entitlement{}, ErrInvalid
	}
	mut, err := m.beginMutation(ctx, p, id)
	if err != nil {
		return billingcontracts.Entitlement{}, err
	}
	defer mut.tx.Rollback(ctx)
	before := map[string]any{"quota": mut.entitlement.Quota, "activeUntil": mut.entitlement.ActiveUntil, "source": mut.entitlement.Source, "version": mut.entitlement.Version}
	e, err := m.billing.GrantAdminTx(ctx, mut.tx, id, in.Version, in.Quota, in.ValidUntil, in.Reason)
	if errors.Is(err, billing.ErrAdminGrantConflict) {
		return e, ErrConflict
	}
	if errors.Is(err, billing.ErrAdminGrant) {
		return e, ErrInvalid
	}
	if err != nil {
		return e, err
	}
	if err := m.audit(ctx, mut.tx, p, id, id, "entitlement.granted", in.Reason, before, map[string]any{"quota": e.Quota, "activeUntil": e.ActiveUntil, "source": e.Source, "version": e.Version}); err != nil {
		return e, err
	}
	return e, mut.tx.Commit(ctx)
}

func (m *Module) RevokeGrant(ctx context.Context, p authcontracts.Principal, id string, version int64, reason string) error {
	if !validReason(reason) || version < 1 {
		return ErrInvalid
	}
	mut, err := m.beginMutation(ctx, p, id)
	if err != nil {
		return err
	}
	defer mut.tx.Rollback(ctx)
	before := map[string]any{"quota": mut.entitlement.Quota, "activeUntil": mut.entitlement.ActiveUntil, "source": mut.entitlement.Source, "version": mut.entitlement.Version}
	if err := m.billing.ExpireAdminTx(ctx, mut.tx, id, version, reason); err != nil {
		if errors.Is(err, billing.ErrAdminGrantConflict) {
			return ErrConflict
		}
		if errors.Is(err, billing.ErrAdminGrant) {
			return ErrInvalid
		}
		return err
	}
	if err := m.audit(ctx, mut.tx, p, id, id, "entitlement.revoked", reason, before, map[string]any{"quota": 0, "source": "admin_grant", "version": version + 1}); err != nil {
		return err
	}
	return mut.tx.Commit(ctx)
}

func (m *Module) ListMonitors(ctx context.Context, id string) ([]monitorcontracts.Monitor, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	if _, err := m.accounts.Get(ctx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return m.monitors.List(ctx, id)
}
func (m *Module) GetMonitor(ctx context.Context, id, monitorID string) (monitorcontracts.Monitor, error) {
	return m.monitors.Get(ctx, id, monitorID)
}
func (m *Module) Results(ctx context.Context, id, monitorID string, limit int, before time.Time) ([]monitorcontracts.Result, error) {
	return m.monitors.Results(ctx, id, monitorID, limit, before)
}
func (m *Module) Incidents(ctx context.Context, id, monitorID string) ([]monitorcontracts.Incident, error) {
	return m.monitors.Incidents(ctx, id, monitorID)
}
func (m *Module) Incident(ctx context.Context, id, monitorID, incidentID string) (monitorcontracts.Incident, error) {
	return m.monitors.Incident(ctx, id, monitorID, incidentID)
}

func (m *Module) CreateMonitor(ctx context.Context, p authcontracts.Principal, id, reason string, in monitorcontracts.MonitorInput) (monitorcontracts.Monitor, error) {
	if !validReason(reason) {
		return monitorcontracts.Monitor{}, ErrInvalid
	}
	mut, err := m.beginMutation(ctx, p, id)
	if err != nil {
		return monitorcontracts.Monitor{}, err
	}
	defer mut.tx.Rollback(ctx)
	monitor, err := m.monitors.CreateTx(ctx, mut.tx, id, mut.entitlement, in)
	if err != nil {
		return monitor, err
	}
	if err := m.audit(ctx, mut.tx, p, id, monitor.ID, "monitor.created", reason, map[string]any{}, map[string]any{"monitorId": monitor.ID, "version": monitor.Version, "enabled": monitor.Enabled}); err != nil {
		return monitor, err
	}
	return monitor, mut.tx.Commit(ctx)
}

func (m *Module) PatchMonitor(ctx context.Context, p authcontracts.Principal, id, monitorID, reason string, in monitorcontracts.Patch) (monitorcontracts.Monitor, error) {
	if !validReason(reason) {
		return monitorcontracts.Monitor{}, ErrInvalid
	}
	mut, err := m.beginMutation(ctx, p, id)
	if err != nil {
		return monitorcontracts.Monitor{}, err
	}
	defer mut.tx.Rollback(ctx)
	before, err := m.monitors.GetTx(ctx, mut.tx, id, monitorID, mut.entitlement)
	if err != nil {
		return monitorcontracts.Monitor{}, err
	}
	monitor, err := m.monitors.PatchTx(ctx, mut.tx, id, monitorID, mut.entitlement, in)
	if err != nil {
		return monitor, err
	}
	if err := m.audit(ctx, mut.tx, p, id, monitorID, "monitor.updated", reason, map[string]any{"monitorId": before.ID, "version": before.Version, "enabled": before.Enabled}, map[string]any{"monitorId": monitor.ID, "version": monitor.Version, "enabled": monitor.Enabled}); err != nil {
		return monitor, err
	}
	return monitor, mut.tx.Commit(ctx)
}

func (m *Module) DeleteMonitor(ctx context.Context, p authcontracts.Principal, id, monitorID string, version int64, reason string) error {
	if !validReason(reason) || version < 1 {
		return ErrInvalid
	}
	mut, err := m.beginMutation(ctx, p, id)
	if err != nil {
		return err
	}
	defer mut.tx.Rollback(ctx)
	before, err := m.monitors.GetTx(ctx, mut.tx, id, monitorID, mut.entitlement)
	if err != nil {
		return err
	}
	if before.Version != version {
		return ErrConflict
	}
	if err := m.monitors.DeleteTx(ctx, mut.tx, id, monitorID); err != nil {
		return err
	}
	if err := m.audit(ctx, mut.tx, p, id, monitorID, "monitor.deleted", reason, map[string]any{"monitorId": before.ID, "version": before.Version, "enabled": before.Enabled}, map[string]any{}); err != nil {
		return err
	}
	return mut.tx.Commit(ctx)
}

func (m *Module) Audit(ctx context.Context, accountID, cursor string, limit int) ([]AuditEvent, string, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || len(cursor) > 256 || (accountID != "" && !validID(accountID)) {
		return nil, "", ErrInvalid
	}
	var beforeTime *time.Time
	var beforeID *string
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, "", ErrInvalid
		}
		parts := strings.Split(string(decoded), "|")
		if len(parts) != 2 || !validID(parts[1]) {
			return nil, "", ErrInvalid
		}
		parsed, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil {
			return nil, "", ErrInvalid
		}
		beforeTime, beforeID = &parsed, &parts[1]
	}
	rows, err := m.pool.Query(ctx, `SELECT id::text,operation_id,actor_account_id::text,actor_session_id::text,target_account_id::text,target_id::text,
		action,reason,before_state,after_state,metadata,created_at FROM admin.audit_events
		WHERE ($1::uuid IS NULL OR target_account_id=$1::uuid)
		AND ($2::timestamptz IS NULL OR (created_at,id)<($2::timestamptz,$3::uuid))
		ORDER BY created_at DESC,id DESC LIMIT $4`, nullableID(accountID), beforeTime, beforeID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := make([]AuditEvent, 0, limit+1)
	for rows.Next() {
		var item AuditEvent
		if err := rows.Scan(&item.ID, &item.OperationID, &item.ActorAccountID, &item.ActorSessionID, &item.TargetAccountID, &item.TargetID, &item.Action, &item.Reason, &item.BeforeState, &item.AfterState, &item.Metadata, &item.CreatedAt); err != nil {
			return nil, "", err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID))
	}
	return items, next, nil
}

func nullableID(value string) any {
	if value == "" {
		return nil
	}
	return value
}
