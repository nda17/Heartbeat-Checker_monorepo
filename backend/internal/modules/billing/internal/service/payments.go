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
	ErrForbidden = errors.New("billing is unavailable for this account")
	ErrNotFound  = errors.New("billing order not found")
	ErrConflict  = errors.New("billing order conflicts with current state")
)

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

const orderColumns = `id,provider,status,monitor_count,amount_minor,currency,created_at,expires_at,completed_at,active_until,confirmation_url,fulfillment_status,fulfillment_reason,COALESCE(provider_mode,''),COALESCE(provider_shop_id,''),COALESCE(provider_mode='test',false),last_error_code,reconciliation_status,purchase_kind,renew_package_id::text,fulfilled_package_id::text,return_target,checkout_kind,(SELECT draft_expires_at FROM billing.sdk_payment_requests WHERE order_id=billing.orders.id),(SELECT request_expires_at FROM billing.sdk_payment_requests WHERE order_id=billing.orders.id),(SELECT payment_method_type FROM billing.sdk_payment_requests WHERE order_id=billing.orders.id)`

func scanOrder(row pgx.Row) (contracts.Order, error) {
	var order contracts.Order
	var draftExpires, requestExpires *time.Time
	var method *string
	err := row.Scan(&order.ID, &order.Provider, &order.Status, &order.MonitorCount, &order.AmountMinor, &order.Currency, &order.CreatedAt, &order.ExpiresAt, &order.CompletedAt, &order.ActiveUntil, &order.ConfirmationURL, &order.FulfillmentStatus, &order.FulfillmentReason, &order.ProviderMode, &order.ProviderShopID, &order.Test, &order.LastErrorCode, &order.ReconciliationStatus, &order.PurchaseKind, &order.PackageID, &order.FulfilledPackageID, &order.ReturnTarget, &order.CheckoutKind, &draftExpires, &requestExpires, &method)
	if err == nil && order.CheckoutKind == "android_sdk" && draftExpires != nil && requestExpires != nil {
		state := "processing"
		switch {
		case order.ReconciliationStatus == "awaiting_token":
			state = "awaiting_token"
		case order.ReconciliationStatus == "manual_review":
			state = "manual_review"
		case order.Status != "pending":
			state = "complete"
		case order.ConfirmationURL != "":
			state = "confirmation_required"
		}
		order.SDK = &contracts.SDKOrder{State: state, ShopID: order.ProviderShopID, Environment: sdkEnvironment(order.ProviderMode), DraftExpiresAt: *draftExpires, RequestExpiresAt: *requestExpires}
		if method != nil {
			order.SDK.PaymentMethodType = *method
		}
	}
	return order, err
}

func (s *Service) Overview(ctx context.Context, accountID string) (contracts.Billing, error) {
	result := contracts.Billing{Provider: s.provider, Orders: []contracts.Order{}, CanPay: s.provider == "yookassa" && s.canPay(accountID), Test: s.provider == "yookassa" && s.mode == "test"}
	if pricing, err := s.Pricing(ctx); err == nil {
		result.PricePerMonitorMinor, result.PriceVersion = pricing.PricePerMonitorMinor, pricing.Version
	}
	result.NativeCheckout = contracts.NativeCheckout{Enabled: s.sdkEnabled && s.canPayNative(accountID), ShopID: s.nativeShopID, Environment: sdkEnvironment(s.nativeMode), PaymentMethods: []string{"BANK_CARD"}}
	rows, err := s.pool.Query(ctx, `SELECT `+orderColumns+` FROM billing.orders WHERE account_id=$1 AND purchase_kind='topup' ORDER BY created_at DESC,id DESC LIMIT 100`, accountID)
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

// ListOrders reads the count and page from one snapshot so concurrent orders
// cannot make pagination disagree with the returned rows.
func (s *Service) ListOrders(ctx context.Context, accountID string, page, pageSize int) (contracts.OrderPage, error) {
	maxInt := int(^uint(0) >> 1)
	if page < 1 || pageSize < 1 || pageSize > 100 || page-1 > maxInt/pageSize {
		return contracts.OrderPage{}, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return contracts.OrderPage{}, err
	}
	defer tx.Rollback(ctx)
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM billing.orders WHERE account_id=$1`, accountID).Scan(&total); err != nil {
		return contracts.OrderPage{}, err
	}
	totalPages := 1
	if total > 0 {
		totalPages = (total-1)/pageSize + 1
	}
	page = min(page, totalPages)
	rows, err := tx.Query(ctx, `SELECT `+orderColumns+` FROM billing.orders WHERE account_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3`, accountID, pageSize, (page-1)*pageSize)
	if err != nil {
		return contracts.OrderPage{}, err
	}
	result := contracts.OrderPage{Orders: []contracts.Order{}, Pagination: contracts.Pagination{Page: page, PageSize: pageSize, Total: total, TotalPages: totalPages}}
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			rows.Close()
			return contracts.OrderPage{}, err
		}
		result.Orders = append(result.Orders, order)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return contracts.OrderPage{}, err
	}
	return result, tx.Commit(ctx)
}
