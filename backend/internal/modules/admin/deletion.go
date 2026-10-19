package admin

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ScrubAccountTx keeps the technical action trail but removes freeform payloads
// that may contain the deleted account's profile, URLs, or contact details.
func ScrubAccountTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	_, err := tx.Exec(ctx, `UPDATE admin.audit_events SET reason='',before_state='{}'::jsonb,
		after_state='{}'::jsonb,metadata='{}'::jsonb,operation_id=NULL,
		actor_session_id=NULL,target_id=NULL
		WHERE actor_account_id=$1 OR target_account_id=$1`, accountID)
	return err
}
