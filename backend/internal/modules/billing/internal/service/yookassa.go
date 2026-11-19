package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgtype"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/internal/adapters/yookassa"
)

func (s *Service) ConfigureYooKassa(provider contracts.Provider, mode, shopID string, ids []string) error {
	if s.provider != "yookassa" || provider == nil || !validProfile(mode, shopID) || len(ids) > 1 || (mode == "test" && len(ids) != 1) || (len(ids) == 1 && !validID(ids[0])) {
		return ErrInvalid
	}
	s.payments, s.mode, s.shopID = provider, mode, shopID
	s.profiles = map[string]paymentProfile{profileKey(mode, shopID): {Mode: mode, ShopID: shopID, Provider: provider}}
	if len(ids) == 1 {
		var owner pgtype.UUID
		_ = owner.Scan(ids[0])
		s.testAllowed = map[string]bool{owner.String(): true}
		if mode == "test" {
			s.allowed = s.testAllowed
		}
	}
	return nil
}

type paymentProfile struct {
	Mode, ShopID string
	Provider     contracts.Provider
}

func validProfile(mode, shopID string) bool {
	return (mode == "test" || mode == "production") && shopID != "" && len(shopID) <= 32 && strings.Trim(shopID, "0123456789") == ""
}

func profileKey(mode, shopID string) string { return mode + ":" + shopID }

func sdkEnvironment(mode string) string {
	if mode == "production" {
		return "live"
	}
	return "test"
}

func (s *Service) ConfigureTestProfile(provider contracts.Provider, shopID string) error {
	if s.provider != "yookassa" || provider == nil || !validProfile("test", shopID) || len(s.testAllowed) != 1 {
		return ErrInvalid
	}
	key := profileKey("test", shopID)
	if _, ok := s.profiles[key]; ok {
		return ErrInvalid
	}
	s.profiles[key] = paymentProfile{Mode: "test", ShopID: shopID, Provider: provider}
	return nil
}

func (s *Service) ConfigureNativeProfile(mode, shopID string) error {
	if !validProfile(mode, shopID) || s.profiles[profileKey(mode, shopID)].Provider == nil {
		return ErrInvalid
	}
	s.nativeMode, s.nativeShopID = mode, shopID
	return nil
}

func (s *Service) profile(mode, shopID string) (paymentProfile, bool) {
	p, ok := s.profiles[profileKey(mode, shopID)]
	return p, ok
}

func (s *Service) testShopID() string {
	for _, p := range s.profiles {
		if p.Mode == "test" {
			return p.ShopID
		}
	}
	return ""
}

func (s *Service) canPayNative(account string) bool {
	if _, ok := s.profile(s.nativeMode, s.nativeShopID); !ok {
		return false
	}
	return s.nativeMode == "production" || (s.nativeMode == "test" && s.testAllowed[account])
}

func (s *Service) canPayOrder(account string, order contracts.Order) bool {
	if _, ok := s.profile(order.ProviderMode, order.ProviderShopID); !ok {
		return false
	}
	return order.ProviderMode == "production" || (order.ProviderMode == "test" && s.testAllowed[account])
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

func (s *Service) CreateTopup(ctx context.Context, account string, amount int, key, returnTarget string) (contracts.Order, error) {
	if amount < MinOrderAmountMinor || !validID(key) || (returnTarget != "web" && returnTarget != "android") {
		return contracts.Order{}, ErrInvalid
	}
	if amount > MaxOrderAmountMinor {
		return contracts.Order{}, ErrOrderAmount
	}
	if s.payments == nil || !s.canPay(account) {
		return contracts.Order{}, ErrForbidden
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.Order{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockAccount(ctx, tx, account); err != nil {
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
		if existing.PurchaseKind != "topup" || existing.CheckoutKind != "redirect" || existing.AmountMinor != amount || existing.ReturnTarget != returnTarget {
			return contracts.Order{}, ErrConflict
		}
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.Order{}, err
	}
	if err = availableTopupSlot(ctx, tx, account); err != nil {
		return contracts.Order{}, err
	}
	id, err := newID()
	if err != nil {
		return contracts.Order{}, err
	}
	returnURL := "https://heartbeat-checker.ru/app"
	if returnTarget == "android" {
		returnURL = "https://heartbeat-checker.ru/android/payments/return?order=" + id
	}
	body, err := json.Marshal(map[string]any{
		"amount":       map[string]string{"value": moneyValue(amount), "currency": "RUB"},
		"capture":      true,
		"confirmation": map[string]string{"type": "redirect", "return_url": returnURL},
		"description":  "Heartbeat Checker: пополнение баланса",
		"metadata":     map[string]string{"order_id": id},
	})
	if err != nil {
		return contracts.Order{}, err
	}
	order, err := scanOrder(tx.QueryRow(ctx, `INSERT INTO billing.orders(id,account_id,idempotency_key,provider,monitor_count,amount_minor,provider_mode,provider_shop_id,provider_request,expires_at,purchase_kind,return_target)
        VALUES($1,$2,$3,'yookassa',0,$4,$5,$6,$7,NULL,'topup',$8) RETURNING `+orderColumns, id, account, key, amount, s.mode, s.shopID, body, returnTarget))
	if err != nil {
		return contracts.Order{}, err
	}
	return order, tx.Commit(ctx)
}

func availableTopupSlot(ctx context.Context, tx pgx.Tx, account string) error {
	if _, err := tx.Exec(ctx, `UPDATE billing.orders SET status='canceled',completed_at=now(),fulfillment_status='not_required',reconciliation_status='complete'
        WHERE account_id=$1 AND purchase_kind='topup' AND checkout_kind='android_sdk' AND status='pending' AND reconciliation_status='awaiting_token'
        AND id IN (SELECT order_id FROM billing.sdk_payment_requests WHERE token_hash IS NULL AND draft_expires_at<=now())`, account); err != nil {
		return err
	}
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing.orders WHERE account_id=$1 AND status='pending' AND purchase_kind='topup')`, account).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return ErrConflict
	}
	return nil
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
	testShop := s.testShopID()
	err = tx.QueryRow(ctx, `SELECT id::text FROM billing.orders WHERE purchase_kind='topup' AND provider='yookassa' AND ((provider_mode=$4 AND provider_shop_id=$2) OR (provider_mode='test' AND provider_shop_id=$5)) AND (provider_payment_id=$1 OR (id=NULLIF($3,'')::uuid AND status='pending' AND (provider_payment_id IS NULL OR provider_payment_id=$1))) ORDER BY (provider_payment_id=$1) DESC NULLS LAST LIMIT 1`, n.Object.ID, s.shopID, hint, s.mode, testShop).Scan(&id)
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
	if _, err = tx.Exec(ctx, `UPDATE billing.orders SET next_reconcile_at=LEAST(next_reconcile_at,now()),reconciliation_status='pending' WHERE id=$1 AND status='pending' AND (reconciliation_status IN ('pending','stopped') OR (checkout_kind='android_sdk' AND reconciliation_status='manual_review'))`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
