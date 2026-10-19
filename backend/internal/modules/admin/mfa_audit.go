package admin

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// AuditMFA is injected into auth so factor changes and privilege elevation
// commit with a minimal audit record. Secrets, codes and their hashes stay out.
func AuditMFA(ctx context.Context, tx pgx.Tx, accountID, sessionID, action, method string, factorVersion int64) error {
	switch action {
	case "mfa.enrollment_started", "mfa.enrolled", "mfa.replaced", "mfa.verified", "mfa.recovered", "mfa.recovery_codes_rotated":
	default:
		return errors.New("invalid MFA audit action")
	}
	if method != "totp" && method != "recovery_code" || factorVersion < 1 {
		return errors.New("invalid MFA audit metadata")
	}
	_, err := tx.Exec(ctx, `INSERT INTO admin.audit_events(actor_account_id,actor_session_id,target_account_id,target_id,action,after_state,metadata)
		VALUES($1,$2,$1,$1,$3,jsonb_build_object('factorVersion',$4::bigint),jsonb_build_object('method',$5::text))`, accountID, sessionID, action, factorVersion, method)
	return err
}
