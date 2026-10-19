package postgres

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
)

type MFAFactor struct {
	Cipher      []byte
	Version     int64
	LastCounter int64
}

type MFAAudit func(context.Context, pgx.Tx, string, string, string, string, int64) error

func counterFresh(counter int64) bool {
	current := time.Now().Unix() / 30
	return counter >= current-1 && counter <= current+1
}

type MFAEnrollment struct {
	ID                string
	AccountID         string
	SessionID         string
	CredentialVersion int64
	FactorVersion     int64
	Cipher            []byte
	ExpiresAt         time.Time
}

func (r *Repository) MFAFactor(ctx context.Context, accountID string) (MFAFactor, error) {
	var f MFAFactor
	err := r.pool.QueryRow(ctx, `SELECT secret_cipher,version,last_used_counter FROM auth.mfa_factors WHERE account_id=$1`, accountID).
		Scan(&f.Cipher, &f.Version, &f.LastCounter)
	return f, err
}

func (r *Repository) MFACodesRemaining(ctx context.Context, accountID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM auth.mfa_recovery_codes WHERE account_id=$1 AND used_at IS NULL`, accountID).Scan(&count)
	return count, err
}

func (r *Repository) MFAEnrollment(ctx context.Context, accountID, enrollmentID string) (MFAEnrollment, error) {
	var e MFAEnrollment
	err := r.pool.QueryRow(ctx, `SELECT id,account_id,session_id,credential_version,factor_version,secret_cipher,expires_at
		FROM auth.mfa_enrollments WHERE account_id=$1 AND id=$2`, accountID, enrollmentID).
		Scan(&e.ID, &e.AccountID, &e.SessionID, &e.CredentialVersion, &e.FactorVersion, &e.Cipher, &e.ExpiresAt)
	return e, err
}

// Credential is always locked before factor and session. Password KDF is done
// by the service before entering this transaction.
func (r *Repository) mfaTx(ctx context.Context, tx pgx.Tx, accountID, sessionID string, c Credential) (MFAFactor, Session, error) {
	var hash string
	var version int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(password_hash,''),version FROM auth.credentials WHERE account_id=$1 FOR UPDATE`, accountID).Scan(&hash, &version); err != nil {
		return MFAFactor{}, Session{}, err
	}
	if hash == "" || hash != c.PasswordHash || version != c.Version {
		return MFAFactor{}, Session{}, ErrConflict
	}
	var f MFAFactor
	err := tx.QueryRow(ctx, `SELECT secret_cipher,version,last_used_counter FROM auth.mfa_factors WHERE account_id=$1 FOR UPDATE`, accountID).
		Scan(&f.Cipher, &f.Version, &f.LastCounter)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return f, Session{}, err
	}
	var s Session
	err = tx.QueryRow(ctx, `SELECT id,account_id,credential_version,mfa_version,mfa_verified_until,mfa_step_up_until FROM auth.sessions
		WHERE id=$1 AND account_id=$2 AND revoked_at IS NULL AND expires_at>now() FOR UPDATE`, sessionID, accountID).
		Scan(&s.ID, &s.AccountID, &s.Version, &s.MFAVersion, &s.MFAVerifiedUntil, &s.MFAStepUpUntil)
	if err != nil {
		return f, s, err
	}
	if s.Version != version {
		return f, s, ErrConflict
	}
	return f, s, nil
}

func (r *Repository) StartMFAEnrollment(ctx context.Context, c Credential, sessionID, enrollmentID string, cipher []byte, factorVersion int64, expiresAt time.Time, audit MFAAudit) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	f, s, err := r.mfaTx(ctx, tx, c.AccountID, sessionID, c)
	if err != nil {
		return err
	}
	if factorVersion != f.Version+1 {
		return ErrConflict
	}
	if f.Version > 0 && (s.MFAVersion == nil || *s.MFAVersion != f.Version || s.MFAStepUpUntil == nil || !s.MFAStepUpUntil.After(time.Now())) {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO auth.mfa_enrollments(id,account_id,session_id,credential_version,factor_version,secret_cipher,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(account_id) DO UPDATE SET
		id=excluded.id,session_id=excluded.session_id,credential_version=excluded.credential_version,
		factor_version=excluded.factor_version,secret_cipher=excluded.secret_cipher,expires_at=excluded.expires_at,
		created_at=now()`, enrollmentID, c.AccountID, sessionID, c.Version, factorVersion, cipher, expiresAt)
	if err != nil {
		return err
	}
	if audit != nil {
		if err := audit(ctx, tx, c.AccountID, sessionID, "mfa.enrollment_started", "totp", factorVersion); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) ConfirmMFAEnrollment(ctx context.Context, c Credential, e MFAEnrollment, sessionID string, newTokenHash []byte, counter int64, recoveryHashes [][]byte, audit MFAAudit) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	f, s, err := r.mfaTx(ctx, tx, c.AccountID, sessionID, c)
	if err != nil {
		return err
	}
	if !counterFresh(counter) || e.FactorVersion != f.Version+1 || e.SessionID != sessionID || e.CredentialVersion != c.Version ||
		(f.Version > 0 && (s.MFAVersion == nil || *s.MFAVersion != f.Version || s.MFAStepUpUntil == nil || !s.MFAStepUpUntil.After(time.Now()))) {
		return ErrConflict
	}
	var current MFAEnrollment
	err = tx.QueryRow(ctx, `SELECT id,account_id,session_id,credential_version,factor_version,secret_cipher,expires_at
		FROM auth.mfa_enrollments WHERE account_id=$1 FOR UPDATE`, c.AccountID).
		Scan(&current.ID, &current.AccountID, &current.SessionID, &current.CredentialVersion, &current.FactorVersion, &current.Cipher, &current.ExpiresAt)
	if err != nil {
		return err
	}
	if current.ID != e.ID || current.SessionID != sessionID || current.CredentialVersion != c.Version || current.FactorVersion != e.FactorVersion || !bytes.Equal(current.Cipher, e.Cipher) || !current.ExpiresAt.After(time.Now()) {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO auth.mfa_factors(account_id,secret_cipher,version,last_used_counter)
		VALUES($1,$2,$3,$4) ON CONFLICT(account_id) DO UPDATE SET secret_cipher=excluded.secret_cipher,
		version=excluded.version,last_used_counter=excluded.last_used_counter,updated_at=now()`, c.AccountID, e.Cipher, e.FactorVersion, counter)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM auth.mfa_recovery_codes WHERE account_id=$1`, c.AccountID); err != nil {
		return err
	}
	for _, hash := range recoveryHashes {
		if _, err = tx.Exec(ctx, `INSERT INTO auth.mfa_recovery_codes(account_id,factor_version,code_hash) VALUES($1,$2,$3)`, c.AccountID, e.FactorVersion, hash); err != nil {
			return err
		}
	}
	if err = r.completeMFASession(ctx, tx, c.AccountID, sessionID, newTokenHash, e.FactorVersion, true); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM auth.mfa_enrollments WHERE account_id=$1`, c.AccountID); err != nil {
		return err
	}
	if audit != nil {
		action := "mfa.enrolled"
		if e.FactorVersion > 1 {
			action = "mfa.replaced"
		}
		if err := audit(ctx, tx, c.AccountID, sessionID, action, "totp", e.FactorVersion); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) completeMFASession(ctx context.Context, tx pgx.Tx, accountID, sessionID string, tokenHash []byte, factorVersion int64, revokeOthers bool) error {
	if revokeOthers {
		if _, err := tx.Exec(ctx, `UPDATE auth.sessions SET revoked_at=now() WHERE account_id=$1 AND id<>$2 AND revoked_at IS NULL`, accountID, sessionID); err != nil {
			return err
		}
	}
	result, err := tx.Exec(ctx, `UPDATE auth.sessions SET token_hash=$3,mfa_version=$4,
		mfa_verified_until=now()+interval '12 hours',mfa_step_up_until=now()+interval '5 minutes'
		WHERE account_id=$1 AND id=$2 AND revoked_at IS NULL AND expires_at>now()`, accountID, sessionID, tokenHash, factorVersion)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (r *Repository) VerifyMFA(ctx context.Context, c Credential, sessionID string, read MFAFactor, counter int64, newTokenHash []byte, audit MFAAudit) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	f, _, err := r.mfaTx(ctx, tx, c.AccountID, sessionID, c)
	if err != nil {
		return err
	}
	if !counterFresh(counter) || f.Version == 0 || f.Version != read.Version || !bytes.Equal(f.Cipher, read.Cipher) || counter <= f.LastCounter {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.mfa_factors SET last_used_counter=$2 WHERE account_id=$1`, c.AccountID, counter); err != nil {
		return err
	}
	if err = r.completeMFASession(ctx, tx, c.AccountID, sessionID, newTokenHash, f.Version, false); err != nil {
		return err
	}
	if audit != nil {
		if err := audit(ctx, tx, c.AccountID, sessionID, "mfa.verified", "totp", f.Version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) RecoverMFA(ctx context.Context, c Credential, sessionID string, codeHash, newTokenHash []byte, audit MFAAudit) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	f, _, err := r.mfaTx(ctx, tx, c.AccountID, sessionID, c)
	if err != nil {
		return err
	}
	if f.Version == 0 {
		return ErrInvalid
	}
	result, err := tx.Exec(ctx, `UPDATE auth.mfa_recovery_codes SET used_at=now()
		WHERE account_id=$1 AND factor_version=$2 AND code_hash=$3 AND used_at IS NULL`, c.AccountID, f.Version, codeHash)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrInvalid
	}
	if err = r.completeMFASession(ctx, tx, c.AccountID, sessionID, newTokenHash, f.Version, true); err != nil {
		return err
	}
	if audit != nil {
		if err := audit(ctx, tx, c.AccountID, sessionID, "mfa.recovered", "recovery_code", f.Version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) ReplaceRecoveryCodes(ctx context.Context, c Credential, sessionID string, read MFAFactor, counter int64, hashes [][]byte, audit MFAAudit) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	f, _, err := r.mfaTx(ctx, tx, c.AccountID, sessionID, c)
	if err != nil {
		return err
	}
	if !counterFresh(counter) || f.Version == 0 || f.Version != read.Version || !bytes.Equal(f.Cipher, read.Cipher) || counter <= f.LastCounter {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.mfa_factors SET last_used_counter=$2 WHERE account_id=$1`, c.AccountID, counter); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM auth.mfa_recovery_codes WHERE account_id=$1`, c.AccountID); err != nil {
		return err
	}
	for _, hash := range hashes {
		if _, err = tx.Exec(ctx, `INSERT INTO auth.mfa_recovery_codes(account_id,factor_version,code_hash) VALUES($1,$2,$3)`, c.AccountID, f.Version, hash); err != nil {
			return err
		}
	}
	if audit != nil {
		if err := audit(ctx, tx, c.AccountID, sessionID, "mfa.recovery_codes_rotated", "totp", f.Version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func RecoveryHashes(codes []string) [][]byte {
	hashes := make([][]byte, 0, len(codes))
	for _, code := range codes {
		hashes = append(hashes, security.Digest(code))
	}
	return hashes
}
