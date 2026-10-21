package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgtype"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/internal/adapters/yookassa"
)

func (s *Service) ConfigureYooKassa(provider contracts.Provider, mode, shopID string, ids []string) error {
	if s.provider != "yookassa" || provider == nil || (mode != "test" && mode != "production") || shopID == "" || len(shopID) > 32 || strings.Trim(shopID, "0123456789") != "" || (mode == "test" && (len(ids) != 1 || !validID(ids[0]))) {
		return ErrInvalid
	}
	s.payments, s.mode, s.shopID = provider, mode, shopID
	if mode == "test" {
		var owner pgtype.UUID
		_ = owner.Scan(ids[0])
		s.allowed = map[string]bool{owner.String(): true}
	}
	return nil
}

func (s *Service) canPay(account string) bool {
	return s.mode == "production" || (s.mode == "test" && s.allowed[account])
}

func (s *Service) GetOrder(ctx context.Context, account, id string) (contracts.Order, error) {
	if !validID(id) {
		return contracts.Order{}, ErrNotFound
	}
	order, err := scanOrder(s.pool.QueryRow(ctx, `SELECT `+orderColumns+` FROM billing.orders WHERE account_id=$1 AND id=$2`, account, id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return order, err
}

func (s *Service) createYooKassaOrder(ctx context.Context, account string, count int, key string, version int64, kind, packageID string) (contracts.Order, error) {
	if count < 1 || count > 1000 || !validID(key) {
		return contracts.Order{}, ErrInvalid
	}
	if s.payments == nil || !s.canPay(account) {
		return contracts.Order{}, ErrForbidden
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.Order{}, err
	}
	defer tx.Rollback(ctx)
	entitlement, err := s.Lock(ctx, tx, account)
	if err != nil {
		return contracts.Order{}, err
	}
	active, err := s.accounts.Active(ctx, tx, account)
	if err != nil {
		return contracts.Order{}, err
	}
	if !active {
		return contracts.Order{}, ErrForbidden
	}
	existing, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderColumns+` FROM billing.orders WHERE account_id=$1 AND idempotency_key=$2`, account, key))
	if err == nil {
		if existing.Provider != "yookassa" || existing.MonitorCount != count || !samePackageSelection(existing, kind, packageID) {
			return contracts.Order{}, ErrConflict
		}
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.Order{}, err
	}
	if s.mode == "test" && activeRealPackage(entitlement, time.Now()) {
		return contracts.Order{}, ErrForbidden
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
	kind, packageID, err = s.selectPackage(entitlement, kind, packageID, count)
	if err != nil {
		return contracts.Order{}, err
	}

	if _, err = tx.Exec(ctx, `UPDATE billing.orders SET status='canceled',completed_at=now(),fulfillment_status='not_required',reconciliation_status='complete' WHERE account_id=$1 AND provider='mock' AND status='pending' AND expires_at<=now()`, account); err != nil {
		return contracts.Order{}, err
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing.orders WHERE account_id=$1 AND status='pending')`, account).Scan(&pending); err != nil {
		return contracts.Order{}, err
	}
	if pending {
		return contracts.Order{}, ErrConflict
	}
	id, err := newID()
	if err != nil {
		return contracts.Order{}, err
	}
	body, err := json.Marshal(map[string]any{
		"amount":       map[string]string{"value": moneyValue(count * pricing.PricePerMonitorMinor), "currency": "RUB"},
		"capture":      true,
		"confirmation": map[string]string{"type": "redirect", "return_url": "https://heartbeat-checker.ru/app"},
		"description":  "Heartbeat Checker: мониторинг",
		"metadata":     map[string]string{"order_id": id},
	})
	if err != nil {
		return contracts.Order{}, err
	}
	order, err := scanOrder(tx.QueryRow(ctx, `INSERT INTO billing.orders(id,account_id,idempotency_key,provider,monitor_count,amount_minor,provider_mode,provider_shop_id,provider_request,expires_at,purchase_kind,renew_package_id) VALUES($1,$2,$3,'yookassa',$4,$5,$6,$7,$8,NULL,$9,NULLIF($10,'')::uuid) RETURNING `+orderColumns, id, account, key, count, count*pricing.PricePerMonitorMinor, s.mode, s.shopID, body, kind, packageID))
	if err != nil {
		return contracts.Order{}, err
	}
	return order, tx.Commit(ctx)
}

func moneyValue(minor int) string { return fmt.Sprintf("%d.%02d", minor/100, minor%100) }

// ReceiveNotification persists only an untrusted reconciliation hint. The
// authoritative payment is fetched by the worker, never trusted from this body.
func (s *Service) ReceiveNotification(ctx context.Context, n contracts.ProviderNotification) error {
	if s.provider != "yookassa" || s.payments == nil {
		return ErrForbidden
	}
	if n.Type != "notification" || (n.Event != "payment.succeeded" && n.Event != "payment.canceled") || !yookassa.ValidPaymentID(n.Object.ID) {
		return ErrInvalid
	}
	hint := n.Object.Metadata.OrderID
	if !validID(hint) {
		hint = ""
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM billing.orders WHERE provider='yookassa' AND provider_mode=$4 AND provider_shop_id=$2 AND (provider_payment_id=$1 OR (id=NULLIF($3,'')::uuid AND status='pending' AND (provider_payment_id IS NULL OR provider_payment_id=$1))) ORDER BY (provider_payment_id=$1) DESC NULLS LAST LIMIT 1`, n.Object.ID, s.shopID, hint, s.mode).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	// Serialize ingestion with finalization: a webhook arriving immediately after
	// a terminal result must be retained as processed, not left as an orphan hint.
	var status, canonicalID string
	if err = tx.QueryRow(ctx, `SELECT status,COALESCE(provider_payment_id,'') FROM billing.orders WHERE id=$1 FOR UPDATE`, id).Scan(&status, &canonicalID); err != nil {
		return err
	}
	processed := status != "pending" || (canonicalID != "" && canonicalID != n.Object.ID)
	result := ""
	if processed {
		result = "rejected"
		if canonicalID == n.Object.ID && n.Event == "payment."+status {
			result = "accepted"
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO billing.webhook_inbox(order_id,payment_id,event,processed_at,result) VALUES($1,$2,$3,CASE WHEN $4 THEN now() ELSE NULL END,$5) ON CONFLICT DO NOTHING`, id, n.Object.ID, n.Event, processed, result); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE billing.orders SET next_reconcile_at=LEAST(next_reconcile_at,now()),reconciliation_status='pending' WHERE id=$1 AND status='pending' AND reconciliation_status IN ('pending','stopped')`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
