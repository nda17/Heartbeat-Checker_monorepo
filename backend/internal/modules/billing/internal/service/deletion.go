package service

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (s *Service) DeleteAccountTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	queries := []string{
		`UPDATE billing.entitlements SET valid_until=now(),renewal_anchor_day=NULL,
			reason='Account deleted',version=version+1 WHERE account_id=$1`,
		`UPDATE billing.orders SET status='canceled',completed_at=now() WHERE account_id=$1 AND status='pending'`,
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
