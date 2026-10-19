package postgres

import (
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	auth "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
)

func EmailForNotificationsTx(ctx context.Context, tx pgx.Tx, accountID string) (string, error) {
	var email string
	var verified *time.Time
	err := tx.QueryRow(ctx, `SELECT email,email_verified_at FROM auth.credentials WHERE account_id=$1 FOR SHARE`, accountID).Scan(&email, &verified)
	if err != nil {
		return "", err
	}
	if !security.ValidEmail(email) || email != security.NormalizeEmail(email) || strings.HasSuffix(email, "@invalid.local") {
		return "", pgx.ErrNoRows
	}
	if verified == nil {
		return "", auth.ErrEmailUnverified
	}
	return email, nil
}

// VerifyAdminTx is the in-transaction authorization fence after the caller has
// locked involved account rows. It detects revoked/rotated sessions and MFA.
func (r *Repository) VerifyAdminTx(ctx context.Context, tx pgx.Tx, p auth.Principal, mutation bool) error {
	var credentialVersion int64
	if err := tx.QueryRow(ctx, `SELECT version FROM auth.credentials WHERE account_id=$1 FOR UPDATE`, p.AccountID).Scan(&credentialVersion); err != nil {
		return err
	}
	var factorVersion int64
	if err := tx.QueryRow(ctx, `SELECT version FROM auth.mfa_factors WHERE account_id=$1 FOR UPDATE`, p.AccountID).Scan(&factorVersion); err != nil {
		return err
	}
	var tokenHash []byte
	var sessionVersion int64
	var mfaVersion *int64
	var verified, stepUp *time.Time
	err := tx.QueryRow(ctx, `SELECT token_hash,credential_version,mfa_version,mfa_verified_until,mfa_step_up_until
		FROM auth.sessions WHERE id=$1 AND account_id=$2 AND revoked_at IS NULL AND expires_at>now() FOR UPDATE`, p.SessionID, p.AccountID).
		Scan(&tokenHash, &sessionVersion, &mfaVersion, &verified, &stepUp)
	if err != nil {
		return err
	}
	if !bytes.Equal(tokenHash, p.TokenHash) || sessionVersion != credentialVersion || mfaVersion == nil || *mfaVersion != factorVersion || verified == nil || !verified.After(time.Now()) {
		return ErrConflict
	}
	if mutation && (stepUp == nil || !stepUp.After(time.Now())) {
		return ErrConflict
	}
	return nil
}

// RevokeAccountSessionsTx invalidates credentials and all existing sessions,
// including pending password-reset links tied to the old version.
func (r *Repository) RevokeAccountSessionsTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	result, err := tx.Exec(ctx, `UPDATE auth.credentials SET version=version+1 WHERE account_id=$1`, accountID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	_, err = tx.Exec(ctx, `UPDATE auth.sessions SET revoked_at=now() WHERE account_id=$1 AND revoked_at IS NULL`, accountID)
	return err
}

func (r *Repository) AdminEmail(ctx context.Context, tx pgx.Tx, accountID string) (string, error) {
	var email string
	err := tx.QueryRow(ctx, `SELECT email FROM auth.credentials WHERE account_id=$1`, accountID).Scan(&email)
	return email, err
}

func (r *Repository) AdminSessionCount(ctx context.Context, tx pgx.Tx, accountID string) (int, error) {
	var count int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM auth.sessions WHERE account_id=$1 AND revoked_at IS NULL AND expires_at>now()`, accountID).Scan(&count)
	return count, err
}
