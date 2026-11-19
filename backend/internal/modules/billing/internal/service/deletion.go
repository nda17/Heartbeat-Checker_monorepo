package service

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (s *Service) DeleteAccountTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	queries := []string{
		`UPDATE billing.sdk_payment_requests SET encrypted_request=NULL WHERE order_id IN (SELECT id FROM billing.orders WHERE account_id=$1 AND checkout_kind='android_sdk' AND first_attempt_at IS NULL)`,
		`UPDATE billing.orders SET status='canceled',completed_at=now(),fulfillment_status='not_required',reconciliation_status='complete' WHERE account_id=$1 AND checkout_kind='android_sdk' AND status='pending' AND first_attempt_at IS NULL`,
		`UPDATE billing.sdk_payment_requests SET encrypted_request=NULL WHERE order_id IN (SELECT id FROM billing.orders WHERE account_id=$1 AND checkout_kind='android_sdk' AND first_attempt_at IS NOT NULL AND provider_payment_id IS NULL)`,
		`UPDATE billing.orders SET reconciliation_status='manual_review',last_error_code='payment_create_uncertain' WHERE account_id=$1 AND checkout_kind='android_sdk' AND status='pending' AND first_attempt_at IS NOT NULL AND provider_payment_id IS NULL`,
		`INSERT INTO billing.wallet_ledger(account_id,kind,units,amount_minor,created_at,hour_start,period_start,period_end)
            SELECT account_id,'usage',-units,-amount_minor,clock_timestamp(),hour_start,from_at,until_at FROM billing.wallet_usage WHERE account_id=$1`,
		`DELETE FROM billing.wallets WHERE account_id=$1`,
		`UPDATE billing.orders SET fulfillment_status='blocked',fulfillment_reason='account_deleted',reconciliation_status=CASE WHEN provider_payment_id IS NULL AND reconcile_token IS NULL AND checkout_kind='redirect' THEN 'stopped' ELSE reconciliation_status END WHERE account_id=$1 AND provider='yookassa' AND status='pending'`,
		`UPDATE billing.orders SET status='canceled',completed_at=now() WHERE account_id=$1 AND provider='mock' AND status='pending'`,
		`UPDATE billing.payment_reports SET status='failed',attempt_token=NULL,lease_until=NULL,
			finished_at=now(),last_error_code='request_invalid'
			WHERE order_id IN (SELECT id FROM billing.orders WHERE account_id=$1) AND status IN ('pending','sending')`,
	}
	for _, query := range queries {
		if _, err := tx.Exec(ctx, query, accountID); err != nil {
			return err
		}
	}
	return nil
}
