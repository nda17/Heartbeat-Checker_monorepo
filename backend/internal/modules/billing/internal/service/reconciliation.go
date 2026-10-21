package service

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/internal/adapters/yookassa"
)

var errCreateWindowExpired = errors.New("payment create window expired")

type paymentAttempt struct {
	CreationDeadline            *time.Time
	LeaseUntil                  time.Time
	Order                       contracts.Order
	Profile                     paymentProfile
	AccountID, Token, PaymentID string
	Request                     []byte
	InboxID                     *int64
	Attempts                    int
	RequestExpiresAt            *time.Time
	RequestClosed               bool
}

// claimPayment releases every transaction lock before making provider requests.
func (s *Service) claimPayment(ctx context.Context) (*paymentAttempt, error) {
	var id, account string
	testShop := s.testShopID()
	err := s.pool.QueryRow(ctx, `SELECT id::text,account_id::text FROM billing.orders WHERE provider='yookassa' AND ((provider_mode=$2 AND provider_shop_id=$1) OR (provider_mode='test' AND provider_shop_id=$3)) AND status='pending' AND reconciliation_status='pending' AND next_reconcile_at<=now() AND (reconcile_lease_until IS NULL OR reconcile_lease_until<=now()) ORDER BY next_reconcile_at,id LIMIT 1`, s.shopID, s.mode, testShop).Scan(&id, &account)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.Lock(ctx, tx, account); err != nil {
		return nil, err
	}
	active, err := s.accounts.Active(ctx, tx, account)
	if err != nil {
		return nil, err
	}
	order, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderColumns+` FROM billing.orders WHERE id=$1 AND provider='yookassa' AND ((provider_mode=$3 AND provider_shop_id=$2) OR (provider_mode='test' AND provider_shop_id=$4)) AND status='pending' AND reconciliation_status='pending' AND next_reconcile_at<=now() AND (reconcile_lease_until IS NULL OR reconcile_lease_until<=now()) FOR UPDATE SKIP LOCKED`, id, s.shopID, s.mode, testShop))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	profile, ok := s.profile(order.ProviderMode, order.ProviderShopID)
	if !ok {
		return nil, ErrInvalid
	}
	a := &paymentAttempt{Order: order, Profile: profile, AccountID: account}
	var deadline *time.Time
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT COALESCE(provider_payment_id,''),provider_request,idempotency_deadline,reconcile_attempts,clock_timestamp() FROM billing.orders WHERE id=$1`, id).Scan(&a.PaymentID, &a.Request, &deadline, &a.Attempts, &now); err != nil {
		return nil, err
	}
	if order.CheckoutKind == "android_sdk" {
		// The order's provider_request is only a nonsecret quote. It must never
		// become the wire CREATE body if the encrypted token is unavailable.
		a.Request = nil
		var sealed, hash []byte
		if err = tx.QueryRow(ctx, `SELECT request_expires_at,encrypted_request,request_hash FROM billing.sdk_payment_requests WHERE order_id=$1 FOR UPDATE`, id).Scan(&a.RequestExpiresAt, &sealed, &hash); err != nil {
			return nil, err
		}
		a.RequestClosed = sealed == nil
		if a.PaymentID == "" && sealed != nil && a.RequestExpiresAt.After(now) {
			a.Request, err = s.openSDK(account, id, order.ProviderMode, order.ProviderShopID, sealed, hash)
			if err != nil {
				return nil, err
			}
		}
	}
	if a.PaymentID == "" {
		var inboxID int64
		err = tx.QueryRow(ctx, `SELECT id,payment_id FROM billing.webhook_inbox WHERE order_id=$1 AND processed_at IS NULL ORDER BY received_at,id LIMIT 1`, id).Scan(&inboxID, &a.PaymentID)
		if err == nil {
			a.InboxID = &inboxID
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	if a.PaymentID == "" && (!active || !s.canPayOrder(account, order)) {
		if order.CheckoutKind == "android_sdk" {
			if _, err = tx.Exec(ctx, `UPDATE billing.sdk_payment_requests SET encrypted_request=NULL WHERE order_id=$1`, id); err != nil {
				return nil, err
			}
			if a.Attempts == 0 {
				_, err = tx.Exec(ctx, `UPDATE billing.orders SET status='canceled',completed_at=now(),fulfillment_status='not_required',reconciliation_status='complete',last_error_code='account_inactive' WHERE id=$1`, id)
			} else {
				_, err = tx.Exec(ctx, `UPDATE billing.orders SET reconciliation_status='manual_review',last_error_code='payment_create_uncertain',fulfillment_status='blocked',fulfillment_reason='account_inactive' WHERE id=$1`, id)
			}
			if err != nil {
				return nil, err
			}
			return nil, tx.Commit(ctx)
		}
		_, err = tx.Exec(ctx, `UPDATE billing.orders SET reconciliation_status='stopped',fulfillment_status='blocked',fulfillment_reason=CASE WHEN fulfillment_reason='account_deleted' THEN fulfillment_reason ELSE 'account_inactive' END,reconcile_token=NULL,reconcile_lease_until=NULL WHERE id=$1`, id)
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	createExpired := a.PaymentID == "" && ((deadline != nil && !deadline.After(now)) || (a.RequestExpiresAt != nil && !a.RequestExpiresAt.After(now)))
	if createExpired {
		if order.CheckoutKind == "android_sdk" {
			_, err = tx.Exec(ctx, `UPDATE billing.sdk_payment_requests SET encrypted_request=NULL WHERE order_id=$1`, id)
			if err != nil {
				return nil, err
			}
		}
		if order.CheckoutKind == "android_sdk" && a.Attempts == 0 {
			_, err = tx.Exec(ctx, `UPDATE billing.orders SET status='canceled',completed_at=now(),fulfillment_status='not_required',reconciliation_status='complete',last_error_code='sdk_draft_expired',reconcile_token=NULL,reconcile_lease_until=NULL WHERE id=$1`, id)
		} else {
			_, err = tx.Exec(ctx, `UPDATE billing.orders SET reconciliation_status='manual_review',last_error_code='payment_create_uncertain',reconcile_token=NULL,reconcile_lease_until=NULL WHERE id=$1`, id)
		}
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	if order.CheckoutKind == "android_sdk" && a.PaymentID == "" && a.RequestClosed {
		_, err = tx.Exec(ctx, `UPDATE billing.orders SET reconciliation_status='manual_review',last_error_code='payment_create_uncertain',reconcile_token=NULL,reconcile_lease_until=NULL WHERE id=$1`, id)
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	if order.CheckoutKind == "android_sdk" && a.PaymentID == "" && len(a.Request) == 0 {
		return nil, ErrSDKUnavailable
	}
	a.Token, err = newID()
	if err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `UPDATE billing.orders SET reconcile_token=$2,reconcile_lease_until=now()+interval '30 seconds',reconcile_attempts=reconcile_attempts+1,first_attempt_at=CASE WHEN $3 THEN COALESCE(first_attempt_at,$4::timestamptz) ELSE first_attempt_at END,idempotency_deadline=CASE WHEN $3 THEN COALESCE(idempotency_deadline,$4::timestamptz+interval '23 hours') ELSE idempotency_deadline END WHERE id=$1 RETURNING reconcile_lease_until,idempotency_deadline`, id, a.Token, a.PaymentID == "", now).Scan(&a.LeaseUntil, &a.CreationDeadline)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	if a.RequestExpiresAt != nil && a.CreationDeadline != nil && a.RequestExpiresAt.Before(*a.CreationDeadline) {
		a.CreationDeadline = a.RequestExpiresAt
	}
	return a, nil
}

func (s *Service) ProcessOnePayment(ctx context.Context) (bool, error) {
	if s.provider != "yookassa" || s.payments == nil {
		return false, nil
	}
	// Pending or quarantined evidence is retained for investigation.
	if _, err := s.pool.Exec(ctx, `DELETE FROM billing.webhook_inbox WHERE id IN (SELECT id FROM billing.webhook_inbox WHERE processed_at<now()-interval '90 days' ORDER BY processed_at,id LIMIT 100)`); err != nil {
		return false, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE billing.sdk_payment_requests SET encrypted_request=NULL WHERE request_expires_at<=now() AND encrypted_request IS NOT NULL`); err != nil {
		return false, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE billing.orders SET status='canceled',completed_at=now(),fulfillment_status='not_required',reconciliation_status='complete' WHERE checkout_kind='android_sdk' AND status='pending' AND reconciliation_status='awaiting_token' AND id IN (SELECT order_id FROM billing.sdk_payment_requests WHERE token_hash IS NULL AND draft_expires_at<=now())`); err != nil {
		return false, err
	}
	a, err := s.claimPayment(ctx)
	if err != nil || a == nil {
		return false, err
	}
	providerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	var payment contracts.ProviderPayment
	if a.PaymentID == "" {
		if a.CreationDeadline == nil || !a.CreationDeadline.After(time.Now()) {
			err = errCreateWindowExpired
		} else if !a.LeaseUntil.After(time.Now()) {
			err = context.DeadlineExceeded
		} else {
			deadline := a.LeaseUntil
			if a.CreationDeadline.Before(deadline) {
				deadline = *a.CreationDeadline
			}
			createCtx, createCancel := context.WithDeadline(providerCtx, deadline)
			if createCtx.Err() != nil {
				err = createCtx.Err()
			} else {
				payment, err = a.Profile.Provider.CreatePayment(createCtx, a.Order.ID, a.Request)
			}
			createCancel()
		}
	} else {
		payment, err = a.Profile.Provider.GetPayment(providerCtx, a.PaymentID)
	}
	cancel()
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	return true, s.finishPayment(finishCtx, *a, payment, err)
}

func (s *Service) validPayment(a paymentAttempt, p contracts.ProviderPayment) bool {
	validURL := p.ConfirmationURL == "" || yookassa.ValidConfirmationURL(p.ConfirmationURL)
	if a.Order.CheckoutKind == "android_sdk" {
		validURL = p.PaymentMethodType == "bank_card" && (p.ConfirmationURL == "" || yookassa.ValidSDKConfirmationURL(p.ConfirmationURL))
	}
	return yookassa.ValidPaymentID(p.ID) && (a.PaymentID == "" || p.ID == a.PaymentID) && p.OrderID == a.Order.ID && p.ShopID == a.Profile.ShopID && p.Test != nil && *p.Test == (a.Profile.Mode == "test") && a.Order.Test == (a.Profile.Mode == "test") && p.AmountValue == moneyValue(a.Order.AmountMinor) && p.Currency == a.Order.Currency && (p.Status == "pending" || p.Status == "waiting_for_capture" || p.Status == "succeeded" || p.Status == "canceled") && (p.Status != "succeeded" || p.Paid) && validURL
}

func (s *Service) finishPayment(ctx context.Context, a paymentAttempt, p contracts.ProviderPayment, providerErr error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	entitlement, err := s.Lock(ctx, tx, a.AccountID)
	if err != nil {
		return err
	}
	active, err := s.accounts.Active(ctx, tx, a.AccountID)
	if err != nil {
		return err
	}
	order, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderColumns+` FROM billing.orders WHERE id=$1 AND account_id=$2 FOR UPDATE`, a.Order.ID, a.AccountID))
	if err != nil {
		return err
	}
	var fenced bool
	var storedID string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(status='pending' AND reconciliation_status='pending' AND reconcile_token=$2 AND reconcile_lease_until>clock_timestamp() AND provider_shop_id=$3 AND provider_mode=$4,false),COALESCE(provider_payment_id,'') FROM billing.orders WHERE id=$1`, order.ID, a.Token, a.Profile.ShopID, a.Profile.Mode).Scan(&fenced, &storedID); err != nil {
		return err
	}
	if !fenced {
		return tx.Commit(ctx)
	}
	invalid := providerErr == nil && (!s.validPayment(a, p) || (storedID != "" && storedID != p.ID))
	if providerErr != nil || invalid {
		if order.CheckoutKind == "android_sdk" && a.PaymentID == "" && a.InboxID == nil && (errors.Is(providerErr, yookassa.ErrPaymentTokenRejected) || errors.Is(providerErr, yookassa.ErrRequestRejected) || errors.Is(providerErr, yookassa.ErrConfiguration)) {
			if _, err = tx.Exec(ctx, `UPDATE billing.sdk_payment_requests SET encrypted_request=NULL WHERE order_id=$1`, order.ID); err != nil {
				return err
			}
			if a.Attempts == 0 {
				code := "payment_token_rejected"
				if errors.Is(providerErr, yookassa.ErrRequestRejected) {
					code = "payment_request_rejected"
				}
				if errors.Is(providerErr, yookassa.ErrConfiguration) {
					code = "payment_configuration_error"
				}
				_, err = tx.Exec(ctx, `UPDATE billing.orders SET status='canceled',completed_at=now(),fulfillment_status='not_required',reconciliation_status='complete',last_error_code=$2,reconcile_token=NULL,reconcile_lease_until=NULL WHERE id=$1`, order.ID, code)
			} else {
				_, err = tx.Exec(ctx, `UPDATE billing.orders SET reconciliation_status='manual_review',last_error_code='payment_create_uncertain',reconcile_token=NULL,reconcile_lease_until=NULL WHERE id=$1`, order.ID)
			}
			if err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		code, state := "payment_provider_unavailable", "pending"
		rejectHint := a.InboxID != nil && (invalid || errors.Is(providerErr, yookassa.ErrInvalidResponse) || errors.Is(providerErr, yookassa.ErrNotFound))
		if rejectHint {
			query := `UPDATE billing.webhook_inbox SET processed_at=now(),result='rejected' WHERE id=$1`
			args := []any{*a.InboxID}
			if a.RequestClosed && errors.Is(providerErr, yookassa.ErrNotFound) {
				// Both succeeded/canceled notifications for one nonexistent ID
				// carry the same disproven routing hint.
				query = `UPDATE billing.webhook_inbox SET processed_at=now(),result='rejected' WHERE order_id=$1 AND payment_id=$2 AND processed_at IS NULL`
				args = []any{order.ID, a.PaymentID}
			}
			if _, err = tx.Exec(ctx, query, args...); err != nil {
				return err
			}
			// A forged hint must not quarantine or bind a legitimate order.
			code = ""
			if a.RequestClosed {
				state = "manual_review"
				code = "payment_create_uncertain"
			}
		} else if a.RequestClosed && a.InboxID != nil {
			// A late hint permits one authoritative GET, never reopening the
			// closed CREATE operation or leaving the order in ordinary pending.
			code, state = "payment_create_uncertain", "manual_review"
		} else if errors.Is(providerErr, errCreateWindowExpired) {
			code, state = "payment_create_uncertain", "manual_review"
		} else if invalid || errors.Is(providerErr, yookassa.ErrInvalidResponse) || (errors.Is(providerErr, yookassa.ErrNotFound) && a.Attempts >= 3) {
			code, state = "payment_provider_response_invalid", "manual_review"
		}
		if order.CheckoutKind == "android_sdk" && state == "manual_review" && a.PaymentID == "" {
			if _, err = tx.Exec(ctx, `UPDATE billing.sdk_payment_requests SET encrypted_request=NULL WHERE order_id=$1`, order.ID); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE billing.orders SET reconcile_token=NULL,reconcile_lease_until=NULL,last_error_code=$2,reconciliation_status=$3,next_reconcile_at=now()+interval '1 minute' WHERE id=$1`, order.ID, code, state)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if a.InboxID != nil {
		if _, err = tx.Exec(ctx, `UPDATE billing.webhook_inbox SET processed_at=now(),result='accepted' WHERE id=$1`, *a.InboxID); err != nil {
			return err
		}
	}
	status, fulfillment, reason := "pending", order.FulfillmentStatus, order.FulfillmentReason
	var until *time.Time
	var completed, fulfilled *time.Time
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	switch p.Status {
	case "canceled":
		status, fulfillment, completed = "canceled", "not_required", &now
	case "succeeded":
		status, completed = "succeeded", &now
		switch {
		case reason == "account_deleted":
			fulfillment = "blocked"
		case !active || !s.canPayOrder(a.AccountID, order):
			fulfillment, reason = "blocked", "account_inactive"
		case order.Test && activeRealPackage(entitlement, now) && !(order.CheckoutKind == "android_sdk" && s.testAllowed[a.AccountID]):
			fulfillment, reason = "blocked", "paid_entitlement"
		default:
			source, entitlementReason := "yookassa", "YooKassa payment"
			if order.Test {
				source, entitlementReason = "yookassa_test", "YooKassa test payment: no money charged"
			}
			packageID, expiry, applyErr := s.applyPackage(ctx, tx, a.AccountID, entitlement, order, now, source, entitlementReason)
			if errors.Is(applyErr, ErrConflict) {
				fulfillment, reason = "blocked", "capacity_changed"
			} else if applyErr != nil {
				return applyErr
			} else {
				until, fulfilled, fulfillment, reason = &expiry, &now, "fulfilled", ""
				order.FulfilledPackageID = &packageID
			}

		}
		if reason != "account_deleted" {
			if _, err = tx.Exec(ctx, `INSERT INTO billing.payment_reports(order_id) VALUES($1) ON CONFLICT DO NOTHING`, order.ID); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(ctx, `UPDATE billing.orders SET provider_payment_id=$2,confirmation_url=CASE WHEN $3='' THEN confirmation_url ELSE $3 END,status=$4,completed_at=$5,active_until=$6,fulfillment_status=$7,fulfillment_reason=$8,fulfilled_at=$9,fulfilled_package_id=$10,reconciliation_status=CASE WHEN $4='pending' THEN 'pending' ELSE 'complete' END,last_error_code='',reconcile_token=NULL,reconcile_lease_until=NULL,next_reconcile_at=now()+interval '1 minute' WHERE id=$1`, order.ID, p.ID, p.ConfirmationURL, status, completed, until, fulfillment, reason, fulfilled, order.FulfilledPackageID)
	if err != nil {
		return err
	}
	if order.CheckoutKind == "android_sdk" {
		if _, err = tx.Exec(ctx, `UPDATE billing.sdk_payment_requests SET encrypted_request=NULL WHERE order_id=$1`, order.ID); err != nil {
			return err
		}
	}
	// Once the canonical ID is bound, no alternative hint may remain pending.
	// Webhook ingestion takes this same order lock before inserting another row.
	if _, err = tx.Exec(ctx, `UPDATE billing.webhook_inbox SET processed_at=now(),result=CASE WHEN payment_id=$2 THEN 'accepted' ELSE 'rejected' END WHERE order_id=$1 AND processed_at IS NULL`, order.ID, p.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
