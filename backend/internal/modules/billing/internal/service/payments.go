package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	accounts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
)

var (
	ErrInvalid   = errors.New("invalid billing request")
	ErrForbidden = errors.New("mock billing is unavailable for this account")
	ErrNotFound  = errors.New("billing order not found")
	ErrConflict  = errors.New("billing order conflicts with current state")
)

var moscow = time.FixedZone("Europe/Moscow", 3*60*60)

// nextMockMonth keeps the original billing day when a shorter month clamps it.
func nextMockMonth(start time.Time, anchorDay int) time.Time {
	local := start.In(moscow)
	year, month, _ := local.Date()
	lastDay := time.Date(year, month+2, 0, 0, 0, 0, 0, moscow).Day()
	if anchorDay > lastDay {
		anchorDay = lastDay
	}
	return time.Date(year, month+1, anchorDay, local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), moscow)
}

func (s *Service) Configure(provider string, ids []string, guard accounts.ActiveGuard) error {
	if provider != "disabled" && provider != "mock" && provider != "yookassa" {
		return ErrInvalid
	}
	if guard == nil {
		return ErrInvalid
	}
	s.provider, s.accounts, s.allowed = provider, guard, make(map[string]bool)
	for _, id := range ids {
		if !validID(id) {
			return ErrInvalid
		}
		var uuid pgtype.UUID
		_ = uuid.Scan(id)
		s.allowed[uuid.String()] = true
	}
	return nil
}

func validID(id string) bool {
	var value pgtype.UUID
	return len(id) == 36 && value.Scan(id) == nil && value.Valid
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

const orderColumns = `id,provider,status,monitor_count,amount_minor,currency,created_at,expires_at,completed_at,active_until,confirmation_url,fulfillment_status,fulfillment_reason,(provider_mode='test'),last_error_code,reconciliation_status`

func scanOrder(row pgx.Row) (contracts.Order, error) {
	var order contracts.Order
	err := row.Scan(&order.ID, &order.Provider, &order.Status, &order.MonitorCount, &order.AmountMinor, &order.Currency, &order.CreatedAt, &order.ExpiresAt, &order.CompletedAt, &order.ActiveUntil, &order.ConfirmationURL, &order.FulfillmentStatus, &order.FulfillmentReason, &order.Test, &order.LastErrorCode, &order.ReconciliationStatus)
	return order, err
}

func (s *Service) Overview(ctx context.Context, accountID string) (contracts.Billing, error) {
	result := contracts.Billing{Provider: s.provider, CanSimulate: s.provider == "mock" && s.allowed[accountID], Orders: []contracts.Order{}, CanPay: s.provider == "yookassa" && s.canPay(accountID), Test: s.provider == "yookassa" && s.mode == "test"}
	// A missing current quote must not hide previously created orders. Zero/zero
	// means unavailable, never a free tariff; new orders still require PricingTx.
	if pricing, err := s.Pricing(ctx); err == nil {
		result.PricePerMonitorMinor, result.PriceVersion = pricing.PricePerMonitorMinor, pricing.Version
	}
	// Paid capacity is additive, so checkout can change access even while an
	// administrative grant is active. Keep the legacy field false on the wire.
	if result.CanPay && result.Test {
		var paidAccess bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing.entitlements WHERE account_id=$1 AND source='yookassa' AND valid_until>now())`, accountID).Scan(&paidAccess); err != nil {
			return result, err
		}
		if result.Test && paidAccess {
			result.CanPay = false
		}
	}
	if result.CanSimulate {
		var protectedAccess bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing.admin_grants WHERE account_id=$1 AND valid_until>now()) OR EXISTS(SELECT 1 FROM billing.entitlements WHERE account_id=$1 AND source='yookassa' AND valid_until>now())`, accountID).Scan(&protectedAccess); err != nil {
			return result, err
		}
		result.CanSimulate = !protectedAccess
	}
	rows, err := s.pool.Query(ctx, `SELECT `+orderColumns+` FROM billing.orders WHERE account_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100`, accountID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return result, err
		}
		result.Orders = append(result.Orders, order)
	}
	return result, rows.Err()
}

// beginMock uses the same account lock as monitor mutations, before any order lock.
func (s *Service) beginMock(ctx context.Context, accountID string) (pgx.Tx, contracts.Entitlement, error) {
	if s.provider != "mock" || !s.allowed[accountID] {
		return nil, contracts.Entitlement{}, ErrForbidden
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, contracts.Entitlement{}, err
	}
	entitlement, err := s.Lock(ctx, tx, accountID)
	if err == nil {
		if (entitlement.Admin != nil && entitlement.Admin.ActiveUntil != nil && entitlement.Admin.ActiveUntil.After(time.Now())) ||
			(s.activeSubscription(accountID, entitlement.Paid, time.Now()) && entitlement.Paid.Source == "yookassa") {
			err = ErrForbidden
		}
	}
	if err == nil {
		var active bool
		active, err = s.accounts.Active(ctx, tx, accountID)
		if err == nil && !active {
			err = ErrForbidden
		}
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, entitlement, err
	}
	return tx, entitlement, nil
}

func (s *Service) CreateOrder(ctx context.Context, accountID string, count int, key string, version int64) (contracts.Order, error) {
	if s.provider == "yookassa" {
		return s.createYooKassaOrder(ctx, accountID, count, key, version)
	}
	if count < 1 || count > 1000 || !validID(key) {
		return contracts.Order{}, ErrInvalid
	}
	tx, entitlement, err := s.beginMock(ctx, accountID)
	if err != nil {
		return contracts.Order{}, err
	}
	defer tx.Rollback(ctx)
	existing, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderColumns+` FROM billing.orders WHERE account_id=$1 AND idempotency_key=$2`, accountID, key))
	if err == nil {
		if existing.Provider != "mock" || existing.MonitorCount != count {
			return contracts.Order{}, ErrConflict
		}
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.Order{}, err
	}
	pricing, err := s.PricingTx(ctx, tx, true)
	if err != nil {
		return contracts.Order{}, err
	}
	if version < 1 || version != pricing.Version {
		return contracts.Order{}, ErrPriceChanged
	}
	if count*pricing.PricePerMonitorMinor > MaxOrderAmountMinor {
		return contracts.Order{}, ErrOrderAmount
	}
	// No proration: an active subscription can only be renewed at the same capacity.
	if s.activeSubscription(accountID, entitlement.Paid, time.Now()) && entitlement.Paid.Quota != count {
		return contracts.Order{}, ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE billing.orders SET status='canceled',completed_at=now() WHERE account_id=$1 AND status='pending' AND expires_at<=now()`, accountID); err != nil {
		return contracts.Order{}, err
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing.orders WHERE account_id=$1 AND status='pending')`, accountID).Scan(&pending); err != nil {
		return contracts.Order{}, err
	}
	if pending {
		return contracts.Order{}, ErrConflict
	}
	id, err := newID()
	if err != nil {
		return contracts.Order{}, err
	}
	order, err := scanOrder(tx.QueryRow(ctx, `INSERT INTO billing.orders(id,account_id,idempotency_key,provider,monitor_count,amount_minor) VALUES($1,$2,$3,'mock',$4,$5) RETURNING `+orderColumns, id, accountID, key, count, count*pricing.PricePerMonitorMinor))
	if err != nil {
		return contracts.Order{}, err
	}
	return order, tx.Commit(ctx)
}

func (s *Service) Simulate(ctx context.Context, accountID, orderID, outcome string) (contracts.Order, error) {
	if !validID(orderID) || (outcome != "succeeded" && outcome != "canceled") {
		return contracts.Order{}, ErrInvalid
	}
	tx, entitlement, err := s.beginMock(ctx, accountID)
	if err != nil {
		return contracts.Order{}, err
	}
	defer tx.Rollback(ctx)
	order, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderColumns+` FROM billing.orders WHERE account_id=$1 AND id=$2 FOR UPDATE`, accountID, orderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return order, ErrNotFound
	}
	if err != nil {
		return order, err
	}
	if order.Provider != "mock" {
		return contracts.Order{}, ErrForbidden
	}
	if order.Status != "pending" {
		if order.Status != outcome {
			return order, ErrConflict
		}
		return order, tx.Commit(ctx)
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return order, err
	}
	if !order.ExpiresAt.After(now) {
		outcome = "canceled"
	}
	var until *time.Time
	if outcome == "succeeded" {
		paid := entitlement.Paid
		paidActive := s.activeSubscription(accountID, paid, now)
		if paidActive && paid.Quota != order.MonitorCount {
			return order, ErrConflict
		}
		start := now
		anchorDay := now.In(moscow).Day()
		if paidActive && paid.Source == "mock_test" {
			start = *paid.ActiveUntil
			if paid.RenewalAnchorDay > 0 {
				anchorDay = paid.RenewalAnchorDay
			}
		}
		expiry := nextMockMonth(start, anchorDay)
		until = &expiry
		_, err = tx.Exec(ctx, `INSERT INTO billing.entitlements(account_id,monitor_limit,valid_until,source,reason,renewal_anchor_day) VALUES($1,$2,$3,'mock_test','TEST payment: no money charged',$4) ON CONFLICT(account_id) DO UPDATE SET monitor_limit=excluded.monitor_limit,valid_until=excluded.valid_until,source='mock_test',renewal_anchor_day=excluded.renewal_anchor_day,version=billing.entitlements.version+1,granted_at=now(),reason=excluded.reason`, accountID, order.MonitorCount, until, anchorDay)
		if err != nil {
			return order, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO billing.payment_reports(order_id) VALUES($1)`, orderID); err != nil {
			return order, err
		}
	}
	order, err = scanOrder(tx.QueryRow(ctx, `UPDATE billing.orders SET status=$3,completed_at=$4::timestamptz,active_until=$5,fulfillment_status=CASE WHEN $3='succeeded' THEN 'fulfilled' ELSE 'not_required' END,fulfilled_at=CASE WHEN $3='succeeded' THEN $4::timestamptz ELSE NULL END,reconciliation_status='complete' WHERE account_id=$1 AND id=$2 RETURNING `+orderColumns, accountID, orderID, outcome, now, until))
	if err != nil {
		return order, err
	}
	return order, tx.Commit(ctx)
}
