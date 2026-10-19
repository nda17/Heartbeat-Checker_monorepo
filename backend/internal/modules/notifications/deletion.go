package notifications

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// DeleteAccountTx runs after monitor removal has cascaded webhook and route
// deliveries. In-flight provider calls may finish, but their rows cannot retry.
func DeleteAccountTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	queries := []string{
		`DELETE FROM notifications.reservations WHERE account_id=$1`,
		`DELETE FROM notifications.messenger_reservations WHERE account_id=$1`,
		`DELETE FROM notifications.messenger_links WHERE account_id=$1`,
		`DELETE FROM notifications.messenger_target_rates r USING notifications.messenger_destinations d
			WHERE d.account_id=$1 AND r.provider=d.provider AND r.recipient_fingerprint=d.recipient_fingerprint
			AND NOT EXISTS (SELECT 1 FROM notifications.messenger_destinations other
				WHERE other.account_id<>$1 AND other.provider=d.provider AND other.recipient_fingerprint=d.recipient_fingerprint)`,
		`DELETE FROM notifications.messenger_destinations WHERE account_id=$1`,
	}
	for _, query := range queries {
		if _, err := tx.Exec(ctx, query, accountID); err != nil {
			return err
		}
	}
	return nil
}
