package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	auth "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
)

var (
	ErrSDKUnavailable     = errors.New("native checkout unavailable")
	ErrSDKDraftExpired    = errors.New("native checkout draft expired")
	ErrSDKTokenConflict   = errors.New("native checkout token conflict")
	ErrSDKCheckoutStarted = errors.New("native checkout already started")
	ErrNativeSession      = errors.New("native session is no longer current")
)

func (s *Service) ConfigureNativeSDK(enabled bool, mailKey string, lease func(context.Context, pgx.Tx, string, string) (time.Time, error)) error {
	if enabled && (s.provider != "yookassa" || lease == nil) {
		return ErrInvalid
	}
	if mailKey != "" {
		key, err := base64.StdEncoding.Strict().DecodeString(mailKey)
		if err != nil || len(key) != 32 {
			return ErrInvalid
		}
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write([]byte("heartbeat:billing:sdk:request:v1"))
		block, err := aes.NewCipher(mac.Sum(nil))
		if err != nil {
			return err
		}
		s.sdkBox, err = cipher.NewGCM(block)
		if err != nil {
			return err
		}
	}
	if enabled && s.sdkBox == nil {
		return ErrInvalid
	}
	s.sdkEnabled, s.sessionLease = enabled, lease
	return nil
}

func (s *Service) nativeLease(ctx context.Context, tx pgx.Tx, p auth.Principal) error {
	if p.ClientKind != "android" || len(p.TokenHash) != sha256.Size || s.sessionLease == nil {
		return ErrNativeSession
	}
	if _, err := s.sessionLease(ctx, tx, p.AccountID, p.SessionID); err != nil {
		return ErrNativeSession
	}
	var current bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth.sessions WHERE id=$1 AND account_id=$2 AND client_kind='android' AND token_hash=$3 AND revoked_at IS NULL AND expires_at>clock_timestamp())`, p.SessionID, p.AccountID, p.TokenHash).Scan(&current); err != nil {
		return err
	}
	if !current {
		return ErrNativeSession
	}
	return nil
}

func (s *Service) CreateNativeOrder(ctx context.Context, p auth.Principal, count int, key string, version int64, kind, packageID string) (contracts.Order, contracts.SDKQuote, error) {
	if count < 1 || count > 1000 || !validID(key) {
		return contracts.Order{}, contracts.SDKQuote{}, ErrInvalid
	}
	if s.provider != "yookassa" {
		return contracts.Order{}, contracts.SDKQuote{}, ErrSDKUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.Order{}, contracts.SDKQuote{}, err
	}
	defer tx.Rollback(ctx)
	entitlement, err := s.Lock(ctx, tx, p.AccountID)
	if err != nil {
		return contracts.Order{}, contracts.SDKQuote{}, err
	}
	if err = s.nativeLease(ctx, tx, p); err != nil {
		return contracts.Order{}, contracts.SDKQuote{}, err
	}
	existing, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderColumns+` FROM billing.orders WHERE account_id=$1 AND idempotency_key=$2`, p.AccountID, key))
	if err == nil {
		if existing.CheckoutKind != "android_sdk" || existing.MonitorCount != count || !samePackageSelection(existing, kind, packageID) {
			return contracts.Order{}, contracts.SDKQuote{}, ErrConflict
		}
		return existing, s.sdkQuote(existing), tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.Order{}, contracts.SDKQuote{}, err
	}
	if !s.sdkEnabled || !s.canPayNative(p.AccountID) {
		return contracts.Order{}, contracts.SDKQuote{}, ErrSDKUnavailable
	}
	active, err := s.accounts.Active(ctx, tx, p.AccountID)
	if err != nil {
		return contracts.Order{}, contracts.SDKQuote{}, err
	}
	if !active {
		return contracts.Order{}, contracts.SDKQuote{}, ErrForbidden
	}
	pricing, err := s.PricingTx(ctx, tx, true)
	if err != nil {
		return contracts.Order{}, contracts.SDKQuote{}, err
	}
	if version < 1 || version != pricing.Version {
		return contracts.Order{}, contracts.SDKQuote{}, ErrPriceChanged
	}
	if count*pricing.PricePerMonitorMinor > MaxOrderAmountMinor {
		return contracts.Order{}, contracts.SDKQuote{}, ErrOrderAmount
	}
	source := "yookassa"
	if s.nativeMode == "test" {
		source = "yookassa_test"
	}
	kind, packageID, err = s.selectPackage(entitlement, kind, packageID, count, source)
	if err != nil {
		return contracts.Order{}, contracts.SDKQuote{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE billing.orders SET status='canceled',completed_at=now(),fulfillment_status='not_required',reconciliation_status='complete' WHERE account_id=$1 AND provider='mock' AND status='pending' AND expires_at<=now()`, p.AccountID); err != nil {
		return contracts.Order{}, contracts.SDKQuote{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE billing.orders SET status='canceled',completed_at=now(),fulfillment_status='not_required',reconciliation_status='complete' WHERE account_id=$1 AND checkout_kind='android_sdk' AND status='pending' AND reconciliation_status='awaiting_token' AND id IN (SELECT order_id FROM billing.sdk_payment_requests WHERE token_hash IS NULL AND draft_expires_at<=now())`, p.AccountID); err != nil {
		return contracts.Order{}, contracts.SDKQuote{}, err
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing.orders WHERE account_id=$1 AND status='pending')`, p.AccountID).Scan(&pending); err != nil {
		return contracts.Order{}, contracts.SDKQuote{}, err
	}
	if pending {
		return contracts.Order{}, contracts.SDKQuote{}, ErrConflict
	}
	id, err := newID()
	if err != nil {
		return contracts.Order{}, contracts.SDKQuote{}, err
	}
	body, err := json.Marshal(map[string]any{"amount": map[string]string{"value": moneyValue(count * pricing.PricePerMonitorMinor), "currency": "RUB"}, "capture": true, "description": "Heartbeat Checker: мониторинг", "metadata": map[string]string{"order_id": id}})
	if err != nil {
		return contracts.Order{}, contracts.SDKQuote{}, err
	}
	order, err := scanOrder(tx.QueryRow(ctx, `INSERT INTO billing.orders(id,account_id,idempotency_key,provider,monitor_count,amount_minor,provider_mode,provider_shop_id,provider_request,expires_at,purchase_kind,renew_package_id,return_target,checkout_kind,reconciliation_status) VALUES($1,$2,$3,'yookassa',$4,$5,$6,$7,$8,NULL,$9,NULLIF($10,'')::uuid,'android','android_sdk','awaiting_token') RETURNING `+orderColumns, id, p.AccountID, key, count, count*pricing.PricePerMonitorMinor, s.nativeMode, s.nativeShopID, body, kind, packageID))
	if err != nil {
		return contracts.Order{}, contracts.SDKQuote{}, err
	}
	var draft, request time.Time
	if err = tx.QueryRow(ctx, `INSERT INTO billing.sdk_payment_requests(order_id,draft_expires_at,request_expires_at) VALUES($1,now()+interval '15 minutes',now()+interval '55 minutes') RETURNING draft_expires_at,request_expires_at`, id).Scan(&draft, &request); err != nil {
		return contracts.Order{}, contracts.SDKQuote{}, err
	}
	order.SDK = &contracts.SDKOrder{State: "awaiting_token", ShopID: order.ProviderShopID, Environment: sdkEnvironment(order.ProviderMode), DraftExpiresAt: draft, RequestExpiresAt: request}
	return order, s.sdkQuote(order), tx.Commit(ctx)
}

func (s *Service) sdkQuote(order contracts.Order) contracts.SDKQuote {
	q := contracts.SDKQuote{ShopID: order.ProviderShopID, Environment: sdkEnvironment(order.ProviderMode), AmountMinor: order.AmountMinor, Currency: order.Currency, Title: "Heartbeat Checker", Subtitle: "Мониторинг", SavePaymentMethod: "off", AllowedPaymentMethods: []string{"BANK_CARD"}}
	if order.SDK != nil {
		q.DraftExpiresAt = order.SDK.DraftExpiresAt
	}
	return q
}

func validPaymentToken(token string) bool {
	if len(token) < 1 || len(token) > 2048 {
		return false
	}
	for i := 0; i < len(token); i++ {
		if token[i] < 0x21 || token[i] > 0x7e {
			return false
		}
	}
	return true
}

func (s *Service) sdkAAD(account, order, mode, shop string, hash []byte) []byte {
	return []byte(fmt.Sprintf("v1|%s|%s|%s|%s|%x", account, order, shop, mode, hash))
}
func (s *Service) sealSDK(account, order, mode, shop string, body, hash []byte) ([]byte, error) {
	if s.sdkBox == nil {
		return nil, ErrSDKUnavailable
	}
	nonce := make([]byte, s.sdkBox.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.sdkBox.Seal(nonce, nonce, body, s.sdkAAD(account, order, mode, shop, hash)), nil
}
func (s *Service) openSDK(account, order, mode, shop string, sealed, hash []byte) ([]byte, error) {
	if s.sdkBox == nil || len(sealed) < s.sdkBox.NonceSize()+s.sdkBox.Overhead() {
		return nil, ErrSDKUnavailable
	}
	nonce := sealed[:s.sdkBox.NonceSize()]
	body, err := s.sdkBox.Open(nil, nonce, sealed[s.sdkBox.NonceSize():], s.sdkAAD(account, order, mode, shop, hash))
	if err != nil {
		return nil, ErrSDKUnavailable
	}
	actual := sha256.Sum256(body)
	if !hmac.Equal(actual[:], hash) {
		return nil, ErrSDKUnavailable
	}
	return body, nil
}

func (s *Service) AttachNativeToken(ctx context.Context, p auth.Principal, id, token, method string) (contracts.Order, error) {
	if !validID(id) {
		return contracts.Order{}, ErrNotFound
	}
	if !validPaymentToken(token) || method != "BANK_CARD" {
		return contracts.Order{}, ErrInvalid
	}
	hash := sha256.Sum256([]byte(token))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.Order{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.Lock(ctx, tx, p.AccountID); err != nil {
		return contracts.Order{}, err
	}
	if err = s.nativeLease(ctx, tx, p); err != nil {
		return contracts.Order{}, err
	}
	order, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderColumns+` FROM billing.orders WHERE id=$1 AND account_id=$2 FOR UPDATE`, id, p.AccountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.Order{}, ErrNotFound
	}
	if err != nil {
		return contracts.Order{}, err
	}
	if order.CheckoutKind != "android_sdk" {
		return contracts.Order{}, ErrNotFound
	}
	var storedHash []byte
	var storedMethod *string
	var draft time.Time
	if err = tx.QueryRow(ctx, `SELECT token_hash,payment_method_type,draft_expires_at FROM billing.sdk_payment_requests WHERE order_id=$1 FOR UPDATE`, id).Scan(&storedHash, &storedMethod, &draft); err != nil {
		return contracts.Order{}, err
	}
	if storedHash != nil {
		if !hmac.Equal(storedHash, hash[:]) || storedMethod == nil || *storedMethod != method {
			return contracts.Order{}, ErrSDKTokenConflict
		}
		return order, tx.Commit(ctx)
	}
	if !s.sdkEnabled || !s.canPayNative(p.AccountID) || order.ProviderMode != s.nativeMode || order.ProviderShopID != s.nativeShopID {
		return contracts.Order{}, ErrSDKUnavailable
	}
	if order.Status != "pending" || order.ReconciliationStatus != "awaiting_token" {
		return contracts.Order{}, ErrSDKCheckoutStarted
	}
	if !draft.After(time.Now()) {
		return contracts.Order{}, ErrSDKDraftExpired
	}
	var base map[string]json.RawMessage
	var orderRequest []byte
	if err = tx.QueryRow(ctx, `SELECT provider_request FROM billing.orders WHERE id=$1`, id).Scan(&orderRequest); err != nil {
		return contracts.Order{}, err
	}
	if err = json.Unmarshal(orderRequest, &base); err != nil {
		return contracts.Order{}, err
	}
	base["payment_token"], _ = json.Marshal(token)
	body, err := json.Marshal(base)
	if err != nil || len(body) > 8192 {
		return contracts.Order{}, ErrInvalid
	}
	requestHash := sha256.Sum256(body)
	sealed, err := s.sealSDK(p.AccountID, id, order.ProviderMode, order.ProviderShopID, body, requestHash[:])
	if err != nil {
		return contracts.Order{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE billing.sdk_payment_requests SET token_hash=$2,request_hash=$3,encrypted_request=$4,payment_method_type=$5,attached_at=now() WHERE order_id=$1`, id, hash[:], requestHash[:], sealed, method); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return contracts.Order{}, ErrSDKTokenConflict
		}
		return contracts.Order{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE billing.orders SET reconciliation_status='pending',next_reconcile_at=now() WHERE id=$1`, id); err != nil {
		return contracts.Order{}, err
	}
	order.ReconciliationStatus = "pending"
	order.SDK.State = "processing"
	order.SDK.PaymentMethodType = method
	return order, tx.Commit(ctx)
}

func (s *Service) CancelNativeOrder(ctx context.Context, p auth.Principal, id string) (contracts.Order, error) {
	if !validID(id) {
		return contracts.Order{}, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return contracts.Order{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.Lock(ctx, tx, p.AccountID); err != nil {
		return contracts.Order{}, err
	}
	if err = s.nativeLease(ctx, tx, p); err != nil {
		return contracts.Order{}, err
	}
	order, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderColumns+` FROM billing.orders WHERE account_id=$1 AND id=$2 FOR UPDATE`, p.AccountID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.Order{}, ErrNotFound
	}
	if err != nil {
		return contracts.Order{}, err
	}
	if order.CheckoutKind != "android_sdk" {
		return contracts.Order{}, ErrNotFound
	}
	var attached bool
	if err = tx.QueryRow(ctx, `SELECT token_hash IS NOT NULL FROM billing.sdk_payment_requests WHERE order_id=$1 FOR UPDATE`, id).Scan(&attached); err != nil {
		return contracts.Order{}, err
	}
	if attached {
		return contracts.Order{}, ErrSDKCheckoutStarted
	}
	if order.Status == "canceled" {
		return order, tx.Commit(ctx)
	}
	if order.Status != "pending" || order.ReconciliationStatus != "awaiting_token" {
		return contracts.Order{}, ErrSDKCheckoutStarted
	}
	order, err = scanOrder(tx.QueryRow(ctx, `UPDATE billing.orders SET status='canceled',completed_at=now(),fulfillment_status='not_required',reconciliation_status='complete' WHERE id=$1 AND first_attempt_at IS NULL RETURNING `+orderColumns, id))
	if err != nil {
		return contracts.Order{}, err
	}
	return order, tx.Commit(ctx)
}
