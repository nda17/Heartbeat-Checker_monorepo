package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
)

var ErrAdminTransferRequired = errors.New("administrator must transfer role before deletion")

type AccountDeletion interface {
	LockAccountTx(context.Context, pgx.Tx, string) (contracts.Account, error)
	ScrubAccountTx(context.Context, pgx.Tx, string) error
}

func (r *Repository) DeleteAccount(ctx context.Context, c Credential, sessionID string, tokenHash []byte, deletion AccountDeletion) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Share the global lock with administrative role mutations.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(73421,1)`); err != nil {
		return err
	}
	a, err := deletion.LockAccountTx(ctx, tx, c.AccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if a.Status != "active" {
		return ErrConflict
	}
	if a.Role == "admin" {
		return ErrAdminTransferRequired
	}
	if err = r.VerifyDeletionTx(ctx, tx, c, sessionID, tokenHash); err != nil {
		return err
	}
	if err = r.DeleteAccountTx(ctx, tx, c); err != nil {
		return err
	}
	if err = deletion.ScrubAccountTx(ctx, tx, c.AccountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// VerifyDeletionTx fences a pre-transaction password check against credential
// rotation and against the exact browser session that authorized the request.
func (r *Repository) VerifyDeletionTx(ctx context.Context, tx pgx.Tx, c Credential, sessionID string, tokenHash []byte) error {
	var hash string
	var version int64
	err := tx.QueryRow(ctx, `SELECT COALESCE(password_hash,''),version FROM auth.credentials
		WHERE account_id=$1 FOR UPDATE`, c.AccountID).Scan(&hash, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if hash == "" || hash != c.PasswordHash || version != c.Version {
		return ErrConflict
	}
	var present int
	err = tx.QueryRow(ctx, `SELECT 1 FROM auth.sessions
		WHERE id=$1 AND account_id=$2 AND token_hash=$3 AND credential_version=$4
			AND revoked_at IS NULL AND expires_at>now() FOR UPDATE`, sessionID, c.AccountID, tokenHash, version).Scan(&present)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	return nil
}

// DeleteAccountTx removes all authentication secrets and pending mail payloads.
// The credential row remains with a synthetic unique email for FK/ledger use.
func (r *Repository) DeleteAccountTx(ctx context.Context, tx pgx.Tx, c Credential) error {
	// Registration resends lock proof before mail. Take the same order before
	// scrubbing an owner's proofs, including sibling pending registrations for
	// its current email, so deletion cannot invert that lock order.
	if _, err := tx.Exec(ctx, `SELECT id FROM auth.email_code_challenges
        WHERE account_id=$1 OR (purpose='registration' AND email=(SELECT email FROM auth.credentials WHERE account_id=$1))
        ORDER BY id FOR UPDATE`, c.AccountID); err != nil {
		return err
	}
	queries := []string{
		`DELETE FROM auth.mail_jobs WHERE token_hash IN (
			SELECT code_hash FROM auth.email_code_challenges WHERE account_id=$1 OR (purpose='registration' AND email=(SELECT email FROM auth.credentials WHERE account_id=$1))
            UNION SELECT token_hash FROM auth.email_verification_tokens WHERE account_id=$1
			UNION SELECT token_hash FROM auth.password_reset_tokens WHERE account_id=$1
			UNION SELECT token_hash FROM auth.registration_challenges
			WHERE email=(SELECT email FROM auth.credentials WHERE account_id=$1)
			OR EXISTS (SELECT 1 FROM auth.oauth_identities i WHERE i.account_id=$1
				AND i.provider=auth.registration_challenges.oauth_provider
				AND i.subject=auth.registration_challenges.oauth_subject))`,
		`DELETE FROM auth.registration_challenges
			WHERE email=(SELECT email FROM auth.credentials WHERE account_id=$1)
			OR EXISTS (SELECT 1 FROM auth.oauth_identities i WHERE i.account_id=$1
				AND i.provider=auth.registration_challenges.oauth_provider
				AND i.subject=auth.registration_challenges.oauth_subject)`,
		`DELETE FROM auth.email_code_challenges WHERE account_id=$1 OR (purpose='registration' AND email=(SELECT email FROM auth.credentials WHERE account_id=$1))`,
		`DELETE FROM auth.email_verification_tokens WHERE account_id=$1`,
		`DELETE FROM auth.password_reset_tokens WHERE account_id=$1`,
		`DELETE FROM auth.oauth_attempts WHERE account_id=$1`,
		`DELETE FROM auth.oauth_identities WHERE account_id=$1`,
		`DELETE FROM auth.mfa_enrollments WHERE account_id=$1`,
		`DELETE FROM auth.sessions WHERE account_id=$1`,
		`DELETE FROM auth.mfa_factors WHERE account_id=$1`,
	}
	for _, query := range queries {
		if _, err := tx.Exec(ctx, query, c.AccountID); err != nil {
			return err
		}
	}
	command, err := tx.Exec(ctx, `UPDATE auth.credentials SET email='deleted+' || account_id::text || '@invalid.local',
		password_hash=NULL,version=version+1,password_changed_at=now(),email_verified_at=NULL
		WHERE account_id=$1 AND version=$2`, c.AccountID, c.Version)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
