package admin

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

// AuditOAuth stores only identity identifiers and the provider, never the
// provider subject, email, credentials or authorization code.
func AuditOAuth(ctx context.Context, tx pgx.Tx, accountID, sessionID, identityID, provider, action string) error {
	if provider != "vk" && provider != "yandex" {
		return errors.New("invalid OAuth audit provider")
	}
	var before, after any = map[string]string{}, map[string]string{}
	state := map[string]string{"identityId": identityID, "provider": provider}
	switch action {
	case "oauth.registered", "oauth.linked":
		after = state
	case "oauth.unlinked":
		before = state
	default:
		return errors.New("invalid OAuth audit action")
	}
	_, err := tx.Exec(ctx, `INSERT INTO admin.audit_events(actor_account_id,actor_session_id,target_account_id,target_id,action,before_state,after_state,metadata) VALUES($1,$2,$1,$3,$4,$5,$6,jsonb_build_object('provider',$7::text))`, accountID, sessionID, identityID, action, before, after, provider)
	return err
}
