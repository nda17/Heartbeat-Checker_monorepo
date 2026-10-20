package notifications

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// FenceAccountDeletionTx runs after the account, credential and authorizing
// session guards, before auth.sessions are deleted. Their FK updates must not
// lock a pairing before its installation while native bootstrap holds the
// installation and expires pairings. A confirmed pairing can reference an
// installation still bound to a different account, so include both ownerships.
func FenceAccountDeletionTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	_, err := tx.Exec(ctx, `SELECT i.id FROM notifications.push_installations i
		WHERE i.account_id=$1 OR EXISTS (
			SELECT 1 FROM notifications.push_pairings q
			WHERE q.installation_id=i.id AND q.account_id=$1)
		ORDER BY i.id FOR UPDATE OF i`, accountID)
	return err
}

// FenceSessionDeletionTx follows the account/session guards owned by auth. Lock
// every dependent installation before session FK updates can touch its pairings.
// The caller commits this retention transaction before taking monitor locks.
func FenceSessionDeletionTx(ctx context.Context, tx pgx.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `SELECT i.id FROM notifications.push_installations i
		WHERE i.session_id=ANY($1::uuid[]) OR EXISTS (
			SELECT 1 FROM notifications.push_pairings q
			WHERE q.installation_id=i.id AND q.session_id=ANY($1::uuid[]))
		ORDER BY i.id FOR UPDATE OF i`, ids)
	return err
}

// DeleteAccountTx runs after monitor removal has cascaded webhook and route
// deliveries. In-flight provider calls may finish, but their rows cannot retry.
func DeleteAccountTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	queries := []string{
		`DELETE FROM notifications.push_pairings WHERE account_id=$1`,
		`DELETE FROM notifications.push_installations WHERE account_id=$1`,
		`DELETE FROM notifications.push_monitor_settings WHERE account_id=$1`,
		`DELETE FROM notifications.push_account_settings WHERE account_id=$1`,
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
