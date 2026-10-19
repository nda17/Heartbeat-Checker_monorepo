package service

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	accounts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	billing "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
	incidents "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/internal/repository/postgres"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
	probe "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
)

var (
	ErrInvalid     = errors.New("invalid request")
	ErrNotFound    = errors.New("monitor not found")
	ErrConflict    = errors.New("monitor version conflict")
	ErrEntitlement = errors.New("entitlement required")
	ErrQuota       = errors.New("monitor quota exceeded")
)

type Service struct {
	repo      *postgres.Repository
	billing   billing.Service
	incidents incidents.Service
	accounts  accounts.ActiveGuard
}

func New(repo *postgres.Repository, billing billing.Service, incidents incidents.Service, accounts accounts.ActiveGuard) *Service {
	return &Service{repo, billing, incidents, accounts}
}

func ValidID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil
}
func scheduling(in *contracts.MonitorInput) error {
	if in.IntervalMinutes == nil {
		value := 5
		in.IntervalMinutes = &value
	}
	if in.FailureThreshold == nil {
		value := 2
		in.FailureThreshold = &value
	}
	if *in.IntervalMinutes < 1 || *in.IntervalMinutes > 10 || *in.FailureThreshold < 1 || *in.FailureThreshold > 10 {
		return ErrInvalid
	}
	return nil
}
func validate(in *contracts.MonitorInput) (string, error) {
	if err := scheduling(in); err != nil {
		return "", err
	}
	in.Name = strings.TrimSpace(in.Name)
	in.URL = strings.TrimSpace(in.URL)
	if in.CheckType == "" {
		in.CheckType = "http"
	}
	if in.CheckType != "http" && in.CheckType != "tcp" && in.CheckType != "icmp" {
		return "", ErrInvalid
	}
	if in.CheckType != "http" {
		if utf8.RuneCountInString(in.Name) < 1 || utf8.RuneCountInString(in.Name) > 100 || !utf8.ValidString(in.Name) || in.URL != "" || in.Method != "" || in.ExpectedStatus != 0 || in.BodyContains != "" {
			return "", ErrInvalid
		}
		host, err := probe.NormalizeHost(in.Host, true)
		if err != nil || (in.CheckType == "tcp" && (in.Port < 1 || in.Port > 65535)) || (in.CheckType == "icmp" && in.Port != 0) {
			return "", ErrInvalid
		}
		in.Host = host
		return net.JoinHostPort(host, "0"), nil
	}
	if in.Host != "" || in.Port != 0 {
		return "", ErrInvalid
	}
	if utf8.RuneCountInString(in.Name) < 1 || utf8.RuneCountInString(in.Name) > 100 || !utf8.ValidString(in.Name) || len(in.URL) > 2048 || len(in.BodyContains) > 1000 || !utf8.ValidString(in.BodyContains) || (in.Method != "GET" && in.Method != "HEAD") || in.ExpectedStatus < 100 || in.ExpectedStatus > 599 || (in.Method == "HEAD" && in.BodyContains != "") {
		return "", ErrInvalid
	}
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return "", ErrInvalid
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if strings.ContainsAny(host, " \t\r\n%") || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "", ErrInvalid
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return "", ErrInvalid
		}
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if (u.Scheme == "http" && port != "80") || (u.Scheme == "https" && port != "443") {
		return "", ErrInvalid
	}
	return net.JoinHostPort(host, port), nil
}
func translate(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func (s *Service) begin(ctx context.Context, owner string) (pgx.Tx, billing.Entitlement, error) {
	tx, err := s.repo.Pool.Begin(ctx)
	if err != nil {
		return nil, billing.Entitlement{}, err
	}
	e, err := s.billing.Lock(ctx, tx, owner)
	if err != nil {
		tx.Rollback(ctx)
		return nil, e, err
	}
	active, err := s.accounts.Active(ctx, tx, owner)
	if err != nil {
		tx.Rollback(ctx)
		return nil, e, err
	}
	if !active {
		e.Quota = 0
	}
	return tx, e, nil
}
func view(m postgres.Row, e billing.Entitlement, rank int) contracts.Monitor {
	v := m.Monitor
	switch {
	case !m.Enabled:
		v.State = "PAUSED"
		v.NextCheckAt = nil
	case e.Quota == 0 || rank > e.Quota:
		v.State = "SUBSCRIPTION_REQUIRED"
		v.NextCheckAt = nil
	case m.LastCheckedAt == nil || time.Since(*m.LastCheckedAt) > time.Duration(2*m.IntervalMinutes)*time.Minute+30*time.Second:
		v.State = "UNKNOWN"
	}
	return v
}
func (s *Service) List(ctx context.Context, owner string) ([]contracts.Monitor, error) {
	tx, e, err := s.begin(ctx, owner)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := s.repo.List(ctx, tx, owner)
	if err != nil {
		return nil, err
	}
	items := make([]contracts.Monitor, 0, len(rows))
	for i, m := range rows {
		items = append(items, view(m, e, i+1))
	}
	return items, tx.Commit(ctx)
}
func (s *Service) Entitlement(ctx context.Context, owner string) (billing.Entitlement, error) {
	tx, e, err := s.begin(ctx, owner)
	if err != nil {
		return e, err
	}
	defer tx.Rollback(ctx)
	e.MonitorCount, err = s.repo.Count(ctx, tx, owner)
	if err != nil {
		return e, err
	}
	return e, tx.Commit(ctx)
}
func (s *Service) Get(ctx context.Context, owner, id string) (contracts.Monitor, error) {
	if !ValidID(id) {
		return contracts.Monitor{}, ErrNotFound
	}
	tx, e, err := s.begin(ctx, owner)
	if err != nil {
		return contracts.Monitor{}, err
	}
	defer tx.Rollback(ctx)
	result, err := s.GetTx(ctx, tx, owner, id, e)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
func (s *Service) GetTx(ctx context.Context, tx pgx.Tx, owner, id string, e billing.Entitlement) (contracts.Monitor, error) {
	if !ValidID(id) {
		return contracts.Monitor{}, ErrNotFound
	}
	m, err := s.repo.Get(ctx, tx, owner, id, false)
	if err != nil {
		return contracts.Monitor{}, translate(err)
	}
	rank, err := s.repo.Rank(ctx, tx, owner, id)
	if err != nil {
		return contracts.Monitor{}, err
	}
	return view(m, e, rank), nil
}
func (s *Service) Create(ctx context.Context, owner string, in contracts.MonitorInput) (contracts.Monitor, error) {
	tx, e, err := s.begin(ctx, owner)
	if err != nil {
		return contracts.Monitor{}, err
	}
	defer tx.Rollback(ctx)
	m, err := s.CreateTx(ctx, tx, owner, e, in)
	if err != nil {
		return m, err
	}
	return m, tx.Commit(ctx)
}

func (s *Service) CreateTx(ctx context.Context, tx pgx.Tx, owner string, e billing.Entitlement, in contracts.MonitorInput) (contracts.Monitor, error) {
	target, err := validate(&in)
	if err != nil {
		return contracts.Monitor{}, err
	}
	if e.Quota == 0 {
		return contracts.Monitor{}, ErrEntitlement
	}
	count, err := s.repo.Count(ctx, tx, owner)
	if err != nil {
		return contracts.Monitor{}, err
	}
	if count >= e.Quota {
		return contracts.Monitor{}, ErrQuota
	}
	id, err := outbox.ID()
	if err != nil {
		return contracts.Monitor{}, err
	}
	if err = s.repo.Insert(ctx, tx, owner, id, target, in); err != nil {
		return contracts.Monitor{}, err
	}
	m, err := s.repo.Get(ctx, tx, owner, id, false)
	if err != nil {
		return contracts.Monitor{}, err
	}
	return view(m, e, count+1), nil
}
func (s *Service) Patch(ctx context.Context, owner, id string, p contracts.Patch) (contracts.Monitor, error) {
	tx, e, err := s.begin(ctx, owner)
	if err != nil {
		return contracts.Monitor{}, err
	}
	defer tx.Rollback(ctx)
	m, err := s.PatchTx(ctx, tx, owner, id, e, p)
	if err != nil {
		return m, err
	}
	return m, tx.Commit(ctx)
}

func (s *Service) PatchTx(ctx context.Context, tx pgx.Tx, owner, id string, e billing.Entitlement, p contracts.Patch) (contracts.Monitor, error) {
	if !ValidID(id) {
		return contracts.Monitor{}, ErrNotFound
	}
	if p.Version < 1 {
		return contracts.Monitor{}, ErrInvalid
	}
	m, err := s.repo.Get(ctx, tx, owner, id, false)
	if err != nil {
		return contracts.Monitor{}, translate(err)
	}
	if m.Version != p.Version {
		return contracts.Monitor{}, ErrConflict
	}
	interval, threshold := m.IntervalMinutes, m.FailureThreshold
	in := contracts.MonitorInput{CheckType: m.CheckType, Host: m.Host, Port: m.Port, Name: m.Name, URL: m.URL, Method: m.Method, ExpectedStatus: m.ExpectedStatus, BodyContains: m.BodyContains, IntervalMinutes: &interval, FailureThreshold: &threshold, Enabled: m.Enabled}
	if p.CheckType != nil && *p.CheckType == "" {
		return contracts.Monitor{}, ErrInvalid
	}
	if p.CheckType != nil && *p.CheckType != m.CheckType {
		in.CheckType, in.Host, in.Port = *p.CheckType, "", 0
		in.URL, in.Method, in.ExpectedStatus, in.BodyContains = "", "", 0, ""
		if in.CheckType == "http" && (p.URL == nil || p.Method == nil || p.ExpectedStatus == nil) {
			return contracts.Monitor{}, ErrInvalid
		}
	}
	if p.Host != nil {
		in.Host = *p.Host
	}
	if p.Port != nil {
		in.Port = *p.Port
	}
	if p.Name != nil {
		in.Name = *p.Name
	}
	if p.URL != nil {
		in.URL = *p.URL
	}
	if p.Method != nil {
		in.Method = *p.Method
	}
	if p.ExpectedStatus != nil {
		in.ExpectedStatus = *p.ExpectedStatus
	}
	if p.BodyContains != nil {
		in.BodyContains = *p.BodyContains
	}
	if p.IntervalMinutes != nil {
		in.IntervalMinutes = p.IntervalMinutes
	}
	if p.FailureThreshold != nil {
		in.FailureThreshold = p.FailureThreshold
	}
	if p.Enabled != nil {
		in.Enabled = *p.Enabled
	}
	target, err := validate(&in)
	if err != nil {
		return contracts.Monitor{}, err
	}
	rank, err := s.repo.Rank(ctx, tx, owner, id)
	if err != nil {
		return contracts.Monitor{}, err
	}
	if in.Enabled && !m.Enabled {
		if e.Quota == 0 {
			return contracts.Monitor{}, ErrEntitlement
		}
		if rank > e.Quota {
			return contracts.Monitor{}, ErrQuota
		}
	}
	checkChanged := in.CheckType != m.CheckType || in.Host != m.Host || in.Port != m.Port || in.URL != m.URL || in.Method != m.Method || in.ExpectedStatus != m.ExpectedStatus || in.BodyContains != m.BodyContains || *in.IntervalMinutes != m.IntervalMinutes || *in.FailureThreshold != m.FailureThreshold
	resetCheck := checkChanged || in.Enabled != m.Enabled
	if err = s.repo.Update(ctx, tx, owner, id, target, in, resetCheck); err != nil {
		return contracts.Monitor{}, err
	}
	if resetCheck {
		reason := ""
		if checkChanged {
			reason = incidents.CloseConfigChanged
		}
		if m.Enabled && !in.Enabled {
			reason = incidents.ClosePaused
		}
		if err = s.incidents.Reset(ctx, tx, id, reason); err != nil {
			return contracts.Monitor{}, err
		}
	}
	m, err = s.repo.Get(ctx, tx, owner, id, false)
	if err != nil {
		return contracts.Monitor{}, err
	}
	return view(m, e, rank), nil
}
func (s *Service) Delete(ctx context.Context, owner, id string) error {
	tx, _, err := s.begin(ctx, owner)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.DeleteTx(ctx, tx, owner, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) DeleteTx(ctx context.Context, tx pgx.Tx, owner, id string) error {
	if !ValidID(id) {
		return ErrNotFound
	}
	var err error
	if _, err = s.repo.Get(ctx, tx, owner, id, false); err != nil {
		return translate(err)
	}
	if err = s.incidents.Reset(ctx, tx, id, incidents.CloseDeleted); err != nil {
		return err
	}
	if err = s.repo.Delete(ctx, tx, owner, id); err != nil {
		return err
	}
	return nil
}
func (s *Service) Results(ctx context.Context, owner, id string, limit int, before time.Time) ([]contracts.Result, error) {
	if _, err := s.Get(ctx, owner, id); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	return s.repo.Results(ctx, owner, id, limit, before)
}
func (s *Service) Incidents(ctx context.Context, owner, id string) ([]contracts.Incident, error) {
	if _, err := s.Get(ctx, owner, id); err != nil {
		return nil, err
	}
	return s.incidents.List(ctx, owner, id)
}
func (s *Service) Incident(ctx context.Context, owner, id, incidentID string) (contracts.Incident, error) {
	if !ValidID(incidentID) {
		return contracts.Incident{}, ErrNotFound
	}
	if _, err := s.Get(ctx, owner, id); err != nil {
		return contracts.Incident{}, err
	}
	i, err := s.incidents.Get(ctx, owner, id, incidentID)
	return i, translate(err)
}

// Claim acquires the global scheduler lock only for the short database transaction.
// Account entitlement locks always precede monitor locks throughout API and worker.
func (s *Service) Claim(ctx context.Context) (*contracts.Attempt, error) {
	candidates, err := s.repo.Candidates(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range candidates {
		tx, err := s.repo.Pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		attempt, err := s.claimOne(ctx, tx, c)
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			tx.Rollback(ctx)
		}
		if err != nil {
			return nil, err
		}
		if attempt != nil {
			return attempt, nil
		}
	}
	return nil, nil
}
func (s *Service) claimOne(ctx context.Context, tx pgx.Tx, c postgres.Candidate) (*contracts.Attempt, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7349281722)`); err != nil {
		return nil, err
	}
	e, err := s.billing.Lock(ctx, tx, c.AccountID)
	if err != nil {
		return nil, err
	}
	active, err := s.accounts.Active(ctx, tx, c.AccountID)
	if err != nil {
		return nil, err
	}
	if !active {
		e.Quota = 0
	}
	m, err := s.repo.Get(ctx, tx, c.AccountID, c.ID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Scheduling timestamps come from PostgreSQL. A small host/DB clock skew
	// must not reject a row that the database has already marked due.
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	if !m.Enabled || m.NextCheckAt == nil || m.NextCheckAt.After(now) || (m.LeaseUntil != nil && m.LeaseUntil.After(now)) {
		return nil, nil
	}
	rank, err := s.repo.Rank(ctx, tx, c.AccountID, c.ID)
	if err != nil {
		return nil, err
	}
	if e.Quota == 0 || rank > e.Quota {
		return nil, s.repo.Defer(ctx, tx, c.AccountID, c.ID)
	}
	capacity, err := s.repo.Capacity(ctx, tx, c.AccountID, m.TargetKey)
	if err != nil || !capacity {
		return nil, err
	}
	run, err := outbox.ID()
	if err != nil {
		return nil, err
	}
	token, err := outbox.ID()
	if err != nil {
		return nil, err
	}
	if err = s.repo.Claim(ctx, tx, c.AccountID, c.ID, run, token); err != nil {
		return nil, err
	}
	return &contracts.Attempt{AccountID: c.AccountID, MonitorID: c.ID, Request: probe.Request{RunID: run, AttemptToken: token, ConfigVersion: m.Version, CheckType: m.CheckType, Host: m.Host, Port: m.Port, URL: m.URL, Method: m.Method, ExpectedStatus: m.ExpectedStatus, BodyContains: m.BodyContains}}, nil
}
func (s *Service) Complete(ctx context.Context, a contracts.Attempt, r probe.Result) (bool, error) {
	if !probe.ValidResult(a.Request, r) || r.RunID != a.Request.RunID || r.AttemptToken != a.Request.AttemptToken || r.ConfigVersion != a.Request.ConfigVersion || r.LatencyMS < 0 || r.LatencyMS > 12000 || (r.Outcome != "UP" && r.Outcome != "DOWN" && r.Outcome != "UNKNOWN") || r.CheckedAt.IsZero() || r.CheckedAt.After(time.Now().Add(5*time.Second)) || r.CheckedAt.Before(time.Now().Add(-30*time.Second)) || len(r.ErrorCode) > 100 || (r.HTTPStatus != 0 && (r.HTTPStatus < 100 || r.HTTPStatus > 599)) {
		return false, ErrInvalid
	}
	tx, e, err := s.begin(ctx, a.AccountID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	m, err := s.repo.Get(ctx, tx, a.AccountID, a.MonitorID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !m.Enabled || m.CheckType != probe.EffectiveCheckType(a.Request.CheckType) || m.Version != a.Request.ConfigVersion || m.RunID == nil || *m.RunID != a.Request.RunID || m.AttemptToken == nil || *m.AttemptToken != a.Request.AttemptToken || m.LeaseUntil == nil || !m.LeaseUntil.After(time.Now()) {
		return false, nil
	}
	rank, err := s.repo.Rank(ctx, tx, a.AccountID, a.MonitorID)
	if err != nil {
		return false, err
	}
	if e.Quota == 0 || rank > e.Quota {
		return false, nil
	}
	eventID, err := outbox.ID()
	if err != nil {
		return false, err
	}
	var status *int
	if r.HTTPStatus != 0 {
		status = &r.HTTPStatus
	}
	if err = s.repo.StoreResult(ctx, tx, m, eventID, r.RunID, r.AttemptToken, r.Outcome, r.CheckedAt, status, r.LatencyMS, r.ErrorCode, r.RoundTripMS); err != nil {
		return false, err
	}
	if err = outbox.Append(ctx, tx, eventID, m.ID, m.AccountID, "CheckCompleted", map[string]any{"resultId": eventID, "sequence": m.Sequence + 1, "checkType": m.CheckType, "outcome": r.Outcome, "checkedAt": r.CheckedAt}, true); err != nil {
		return false, err
	}
	if err = s.incidents.Apply(ctx, tx, m.ID, m.AccountID, m.Sequence+1, r.Outcome, m.FailureThreshold, r.CheckedAt, incidents.Observation{
		Name: m.Name, CheckType: m.CheckType, Host: m.Host, Port: m.Port, URL: m.URL, Outcome: r.Outcome, HTTPStatus: r.HTTPStatus, RoundTripMS: r.RoundTripMS,
		ErrorCode: r.ErrorCode, LatencyMS: r.LatencyMS, CheckedAt: r.CheckedAt, Sequence: m.Sequence + 1,
	}); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
func (s *Service) Cleanup(ctx context.Context, tx pgx.Tx) error { return s.repo.Cleanup(ctx, tx) }
