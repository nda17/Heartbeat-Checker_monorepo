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
	AccountID, Token, PaymentID string
	Request                     []byte
	InboxID                     *int64
	Attempts                    int
}

// claimPayment releases every transaction lock before making provider requests.
func (s *Service) claimPayment(ctx context.Context) (*paymentAttempt, error) {
	var id, account string
	err := s.pool.QueryRow(ctx, `SELECT id::text,account_id::text FROM billing.orders WHERE provider='yookassa' AND provider_mode=$2 AND provider_shop_id=$1 AND status='pending' AND reconciliation_status='pending' AND next_reconcile_at<=now() AND (reconcile_lease_until IS NULL OR reconcile_lease_until<=now()) ORDER BY next_reconcile_at,id LIMIT 1`, s.shopID, s.mode).Scan(&id, &account)
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
	order, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderColumns+` FROM billing.orders WHERE id=$1 AND provider='yookassa' AND provider_mode=$3 AND provider_shop_id=$2 AND status='pending' AND reconciliation_status='pending' AND next_reconcile_at<=now() AND (reconcile_lease_until IS NULL OR reconcile_lease_until<=now()) FOR UPDATE SKIP LOCKED`, id, s.shopID, s.mode))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	a := &paymentAttempt{Order: order, AccountID: account}
	var deadline *time.Time
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT COALESCE(provider_payment_id,''),provider_request,idempotency_deadline,reconcile_attempts,clock_timestamp() FROM billing.orders WHERE id=$1`, id).Scan(&a.PaymentID, &a.Request, &deadline, &a.Attempts, &now); err != nil {
		return nil, err
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
	if a.PaymentID == "" && (!active || !s.canPay(account)) {
		_, err = tx.Exec(ctx, `UPDATE billing.orders SET reconciliation_status='stopped',fulfillment_status='blocked',fulfillment_reason=CASE WHEN fulfillment_reason='account_deleted' THEN fulfillment_reason ELSE 'account_inactive' END,reconcile_token=NULL,reconcile_lease_until=NULL WHERE id=$1`, id)
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	if a.PaymentID == "" && deadline != nil && !deadline.After(now) {
		_, err = tx.Exec(ctx, `UPDATE billing.orders SET reconciliation_status='manual_review',last_error_code='payment_create_uncertain',reconcile_token=NULL,reconcile_lease_until=NULL WHERE id=$1`, id)
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
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
				payment, err = s.payments.CreatePayment(createCtx, a.Order.ID, a.Request)
			}
			createCancel()
		}
	} else {
		payment, err = s.payments.GetPayment(providerCtx, a.PaymentID)
	}
	cancel()
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	return true, s.finishPayment(finishCtx, *a, payment, err)
}

func (s *Service) validPayment(a paymentAttempt, p contracts.ProviderPayment) bool {
	return yookassa.ValidPaymentID(p.ID) && (a.PaymentID == "" || p.ID == a.PaymentID) && p.OrderID == a.Order.ID && p.ShopID == s.shopID && p.Test != nil && *p.Test == (s.mode == "test") && a.Order.Test == (s.mode == "test") && p.AmountValue == moneyValue(a.Order.AmountMinor) && p.Currency == a.Order.Currency && (p.Status == "pending" || p.Status == "waiting_for_capture" || p.Status == "succeeded" || p.Status == "canceled") && (p.Status != "succeeded" || p.Paid) && (p.ConfirmationURL == "" || yookassa.ValidConfirmationURL(p.ConfirmationURL))
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
	if err = tx.QueryRow(ctx, `SELECT COALESCE(status='pending' AND reconciliation_status='pending' AND reconcile_token=$2 AND reconcile_lease_until>clock_timestamp() AND provider_shop_id=$3 AND provider_mode=$4,false),COALESCE(provider_payment_id,'') FROM billing.orders WHERE id=$1`, order.ID, a.Token, s.shopID, s.mode).Scan(&fenced, &storedID); err != nil {
		return err
	}
	if !fenced {
		return tx.Commit(ctx)
	}
	invalid := providerErr == nil && (!s.validPayment(a, p) || (storedID != "" && storedID != p.ID))
	if providerErr != nil || invalid {
		code, state := "payment_provider_unavailable", "pending"
		rejectHint := a.InboxID != nil && (invalid || errors.Is(providerErr, yookassa.ErrInvalidResponse) || errors.Is(providerErr, yookassa.ErrNotFound))
		if rejectHint {
			if _, err = tx.Exec(ctx, `UPDATE billing.webhook_inbox SET processed_at=now(),result='rejected' WHERE id=$1`, *a.InboxID); err != nil {
				return err
			}
			// A forged hint must not quarantine or bind a legitimate order.
			code = ""
		} else if errors.Is(providerErr, errCreateWindowExpired) {
			code, state = "payment_create_uncertain", "manual_review"
		} else if invalid || errors.Is(providerErr, yookassa.ErrInvalidResponse) || (errors.Is(providerErr, yookassa.ErrNotFound) && a.Attempts >= 3) {
			code, state = "payment_provider_response_invalid", "manual_review"
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
		case !active || !s.canPay(a.AccountID):
			fulfillment, reason = "blocked", "account_inactive"
		case order.Test && entitlement.Source == "yookassa" && entitlement.ActiveUntil != nil && entitlement.ActiveUntil.After(now):
			fulfillment, reason = "blocked", "paid_entitlement"
		case entitlement.Source == "admin_grant" && entitlement.ActiveUntil != nil && entitlement.ActiveUntil.After(now):
			fulfillment, reason = "blocked", "admin_grant"
		case entitlement.Quota > 0 && entitlement.Quota != order.MonitorCount:
			fulfillment, reason = "blocked", "capacity_changed"
		default:
			start, anchor := now, now.In(moscow).Day()
			source := "yookassa"
			entitlementReason := "YooKassa payment"
			if order.Test {
				source, entitlementReason = "yookassa_test", "YooKassa test payment: no money charged"
			}
			if entitlement.Source == source && entitlement.Quota > 0 && entitlement.ActiveUntil != nil && entitlement.ActiveUntil.After(start) {
				start = *entitlement.ActiveUntil
				if entitlement.RenewalAnchorDay > 0 {
					anchor = entitlement.RenewalAnchorDay
				}
			}
			expiry := nextMockMonth(start, anchor)
			until, fulfilled, fulfillment, reason = &expiry, &now, "fulfilled", ""
			_, err = tx.Exec(ctx, `INSERT INTO billing.entitlements(account_id,monitor_limit,valid_until,source,reason,renewal_anchor_day) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(account_id) DO UPDATE SET monitor_limit=excluded.monitor_limit,valid_until=excluded.valid_until,source=excluded.source,renewal_anchor_day=excluded.renewal_anchor_day,version=billing.entitlements.version+1,granted_at=now(),reason=excluded.reason`, a.AccountID, order.MonitorCount, until, source, entitlementReason, anchor)
			if err != nil {
				return err
			}
		}
		if reason != "account_deleted" {
			if _, err = tx.Exec(ctx, `INSERT INTO billing.payment_reports(order_id) VALUES($1) ON CONFLICT DO NOTHING`, order.ID); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(ctx, `UPDATE billing.orders SET provider_payment_id=$2,confirmation_url=CASE WHEN $3='' THEN confirmation_url ELSE $3 END,status=$4,completed_at=$5,active_until=$6,fulfillment_status=$7,fulfillment_reason=$8,fulfilled_at=$9,reconciliation_status=CASE WHEN $4='pending' THEN 'pending' ELSE 'complete' END,last_error_code='',reconcile_token=NULL,reconcile_lease_until=NULL,next_reconcile_at=now()+interval '1 minute' WHERE id=$1`, order.ID, p.ID, p.ConfirmationURL, status, completed, until, fulfillment, reason, fulfilled)
	if err != nil {
		return err
	}
	// Once the canonical ID is bound, no alternative hint may remain pending.
	// Webhook ingestion takes this same order lock before inserting another row.
	if _, err = tx.Exec(ctx, `UPDATE billing.webhook_inbox SET processed_at=now(),result=CASE WHEN payment_id=$2 THEN 'accepted' ELSE 'rejected' END WHERE order_id=$1 AND processed_at IS NULL`, order.ID, p.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
