package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
)

type OAuthAudit func(context.Context, pgx.Tx, string, string, string, string, string) error
type OAuthAccountLock func(context.Context, pgx.Tx, string) (contracts.Account, error)
type OAuthAttempt struct {
	Provider, Mode, ReturnTo string
	StateHash, PKCEChallenge []byte
	AccountID, SessionID     *string
	CredentialVersion        *int64
	SessionTokenHash         []byte
	ExpiresAt                time.Time
}
type OAuthIdentity struct {
	ID          string    `json:"id"`
	Provider    string    `json:"provider"`
	ConnectedAt time.Time `json:"connectedAt"`
	AccountID   string    `json:"-"`
	Subject     string    `json:"-"`
}
type OAuthRegistration struct {
	ID, Provider, Subject, Email, Name, ReturnTo string
	BrowserHash, TokenHash                       []byte
	ExpiresAt                                    time.Time
}
type OAuthSession struct {
	ID, IP, Agent string
	Hash          []byte
	ExpiresAt     time.Time
}

func oauthConflict(err error) error {
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "23505" {
		return ErrAlreadyExists
	}
	return err
}
func (r *Repository) InsertOAuthAttempt(ctx context.Context, a OAuthAttempt, previous []byte, credential Credential, lock OAuthAccountLock) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if a.Mode == "link" {
		if err = oauthLinkFence(ctx, tx, a, credential, lock); err != nil {
			return err
		}
	}
	if len(previous) > 0 {
		if _, err = tx.Exec(ctx, `UPDATE auth.oauth_attempts SET consumed_at=now() WHERE state_hash=$1 AND provider=$2 AND consumed_at IS NULL`, previous, a.Provider); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO auth.oauth_attempts(state_hash,provider,mode,pkce_challenge,return_to,account_id,session_id,credential_version,session_token_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, a.StateHash, a.Provider, a.Mode, a.PKCEChallenge, a.ReturnTo, a.AccountID, a.SessionID, a.CredentialVersion, a.SessionTokenHash, a.ExpiresAt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) ConsumeOAuthAttempt(ctx context.Context, provider string, stateHash, challenge []byte) (OAuthAttempt, error) {
	var a OAuthAttempt
	err := r.pool.QueryRow(ctx, `UPDATE auth.oauth_attempts SET consumed_at=now() WHERE state_hash=$1 AND provider=$2 AND pkce_challenge=$3 AND consumed_at IS NULL AND expires_at>now() RETURNING provider,mode,return_to,account_id,session_id,credential_version,session_token_hash,expires_at`, stateHash, provider, challenge).Scan(&a.Provider, &a.Mode, &a.ReturnTo, &a.AccountID, &a.SessionID, &a.CredentialVersion, &a.SessionTokenHash, &a.ExpiresAt)
	return a, err
}
func (r *Repository) OAuthIdentity(ctx context.Context, provider, subject string) (OAuthIdentity, error) {
	var i OAuthIdentity
	err := r.pool.QueryRow(ctx, `SELECT id,account_id,provider,subject,created_at FROM auth.oauth_identities WHERE provider=$1 AND subject=$2`, provider, subject).Scan(&i.ID, &i.AccountID, &i.Provider, &i.Subject, &i.ConnectedAt)
	return i, err
}
func (r *Repository) OAuthIdentities(ctx context.Context, accountID string) ([]OAuthIdentity, error) {
	rows, err := r.pool.Query(ctx, `SELECT id,provider,created_at FROM auth.oauth_identities WHERE account_id=$1 ORDER BY created_at,id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]OAuthIdentity, 0)
	for rows.Next() {
		var i OAuthIdentity
		if err = rows.Scan(&i.ID, &i.Provider, &i.ConnectedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// OAuth login locks credentials before the identity and session insertion. Password
// reset, unlink, block and role changes therefore fence concurrent callbacks.
func (r *Repository) OAuthLoginSession(ctx context.Context, i OAuthIdentity, c Credential, session OAuthSession, lock OAuthAccountLock, previousHash []byte) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	account, err := lock(ctx, tx, i.AccountID)
	if err != nil {
		return err
	}
	if account.Status != "active" {
		return ErrConflict
	}
	var version int64
	if err = tx.QueryRow(ctx, `SELECT version FROM auth.credentials WHERE account_id=$1 FOR UPDATE`, i.AccountID).Scan(&version); err != nil {
		return err
	}
	if version != c.Version {
		return ErrConflict
	}
	var id string
	if err = tx.QueryRow(ctx, `SELECT id FROM auth.oauth_identities WHERE id=$1 AND account_id=$2 AND provider=$3 AND subject=$4 FOR UPDATE`, i.ID, i.AccountID, i.Provider, i.Subject).Scan(&id); err != nil {
		return err
	}
	if err = insertOAuthSession(ctx, tx, i.AccountID, version, session); err != nil {
		return err
	}
	if err = RevokeReplacedSessionTx(ctx, tx, previousHash); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func insertOAuthSession(ctx context.Context, tx pgx.Tx, accountID string, version int64, s OAuthSession) error {
	_, err := tx.Exec(ctx, `INSERT INTO auth.sessions(id,account_id,token_hash,credential_version,expires_at,ip,user_agent) VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::inet,$7)`, s.ID, accountID, s.Hash, version, s.ExpiresAt, s.IP, s.Agent)
	return err
}
func (r *Repository) InsertOAuthRegistration(ctx context.Context, c OAuthRegistration, payload []byte, jobID string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO auth.registration_challenges(id,email,name,password_hash,token_hash,expires_at,oauth_provider,oauth_subject,oauth_browser_hash,oauth_return_to) VALUES($1,$2,$3,NULL,$4,$5,$6,$7,$8,$9)`, c.ID, c.Email, c.Name, c.TokenHash, c.ExpiresAt, c.Provider, c.Subject, c.BrowserHash, c.ReturnTo)
	if err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err = tx.Exec(ctx, `INSERT INTO auth.mail_jobs(id,purpose,token_hash,payload,expires_at) VALUES($1,'registration',$2,$3,$4)`, jobID, c.TokenHash, payload, c.ExpiresAt)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (r *Repository) OAuthRegistration(ctx context.Context, browserHash []byte) (OAuthRegistration, error) {
	var c OAuthRegistration
	err := r.pool.QueryRow(ctx, `SELECT id,oauth_provider,oauth_subject,email,name,oauth_return_to,expires_at FROM auth.registration_challenges WHERE oauth_browser_hash=$1 AND oauth_provider IS NOT NULL AND consumed_at IS NULL AND expires_at>now() ORDER BY created_at DESC LIMIT 1`, browserHash).Scan(&c.ID, &c.Provider, &c.Subject, &c.Email, &c.Name, &c.ReturnTo, &c.ExpiresAt)
	return c, err
}
func (r *Repository) CompleteOAuthRegistration(ctx context.Context, tokenHash, browserHash []byte, identityID string, session OAuthSession, create func(context.Context, pgx.Tx, string) (string, error), audit OAuthAudit, previousHash []byte) (string, string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx)
	var c OAuthRegistration
	err = tx.QueryRow(ctx, `SELECT id,oauth_provider,oauth_subject,email,name,oauth_return_to,expires_at FROM auth.registration_challenges WHERE token_hash=$1 AND oauth_browser_hash=$2 AND password_hash IS NULL AND oauth_provider IS NOT NULL AND consumed_at IS NULL AND expires_at>now() FOR UPDATE`, tokenHash, browserHash).Scan(&c.ID, &c.Provider, &c.Subject, &c.Email, &c.Name, &c.ReturnTo, &c.ExpiresAt)
	if err != nil {
		return "", "", err
	}
	var fresh bool
	if err = tx.QueryRow(ctx, `SELECT $1::timestamptz>clock_timestamp()`, c.ExpiresAt).Scan(&fresh); err != nil {
		return "", "", err
	}
	if !fresh {
		return "", "", pgx.ErrNoRows
	}
	accountID, err := create(ctx, tx, c.Name)
	if err != nil {
		return "", "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth.credentials(account_id,email,password_hash,email_verified_at) VALUES($1,$2,NULL,now())`, accountID, c.Email); err != nil {
		return "", "", oauthConflict(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth.oauth_identities(id,account_id,provider,subject) VALUES($1,$2,$3,$4)`, identityID, accountID, c.Provider, c.Subject); err != nil {
		return "", "", oauthConflict(err)
	}
	if err = insertOAuthSession(ctx, tx, accountID, 1, session); err != nil {
		return "", "", err
	}
	consumed, err := tx.Exec(ctx, `UPDATE auth.registration_challenges SET consumed_at=clock_timestamp() WHERE id=$1 AND consumed_at IS NULL AND expires_at>clock_timestamp()`, c.ID)
	if err != nil {
		return "", "", err
	}
	if consumed.RowsAffected() != 1 {
		return "", "", pgx.ErrNoRows
	}
	if audit != nil {
		if err = audit(ctx, tx, accountID, session.ID, identityID, c.Provider, "oauth.registered"); err != nil {
			return "", "", err
		}
	}
	if err = RevokeReplacedSessionTx(ctx, tx, previousHash); err != nil {
		return "", "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return accountID, c.ReturnTo, nil
}

func (r *Repository) LinkOAuth(ctx context.Context, a OAuthAttempt, c Credential, id, provider, subject string, newHash []byte, lock OAuthAccountLock, audit OAuthAudit) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = oauthLinkFence(ctx, tx, a, c, lock); err != nil {
		return err
	}
	var oldID, oldSubject string
	err = tx.QueryRow(ctx, `SELECT id,subject FROM auth.oauth_identities WHERE account_id=$1 AND provider=$2 FOR UPDATE`, c.AccountID, provider).Scan(&oldID, &oldSubject)
	if err == nil {
		if oldSubject == subject {
			if err = rotateOAuthSession(ctx, tx, a, c, newHash); err != nil {
				return err
			}
			if audit != nil {
				if err = audit(ctx, tx, c.AccountID, *a.SessionID, oldID, provider, "oauth.linked"); err != nil {
					return err
				}
			}
			return tx.Commit(ctx)
		}
		return ErrAlreadyExists
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO auth.oauth_identities(id,account_id,provider,subject) VALUES($1,$2,$3,$4)`, id, c.AccountID, provider, subject)
	if err != nil {
		return oauthConflict(err)
	}
	if err = rotateOAuthSession(ctx, tx, a, c, newHash); err != nil {
		return err
	}
	if audit != nil {
		if err = audit(ctx, tx, c.AccountID, *a.SessionID, id, provider, "oauth.linked"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func oauthLinkFence(ctx context.Context, tx pgx.Tx, a OAuthAttempt, c Credential, lock OAuthAccountLock) error {
	if a.AccountID == nil || a.SessionID == nil || a.CredentialVersion == nil || *a.AccountID != c.AccountID {
		return ErrConflict
	}
	account, err := lock(ctx, tx, c.AccountID)
	if err != nil {
		return err
	}
	if account.Status != "active" {
		return ErrConflict
	}
	var hash string
	var version int64
	err = tx.QueryRow(ctx, `SELECT COALESCE(password_hash,''),version FROM auth.credentials WHERE account_id=$1 FOR UPDATE`, c.AccountID).Scan(&hash, &version)
	if err != nil {
		return err
	}
	if hash == "" || hash != c.PasswordHash || version != c.Version || version != *a.CredentialVersion {
		return ErrConflict
	}
	var factorVersion *int64
	if account.Role == "admin" {
		err = tx.QueryRow(ctx, `SELECT version FROM auth.mfa_factors WHERE account_id=$1 FOR SHARE`, c.AccountID).Scan(&factorVersion)
		if err != nil {
			return ErrConflict
		}
	}
	var sessionVersion int64
	var mfaVersion *int64
	var verified, stepup *time.Time
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `SELECT credential_version,mfa_version,mfa_verified_until,mfa_step_up_until,expires_at FROM auth.sessions WHERE id=$1 AND account_id=$2 AND token_hash=$3 AND revoked_at IS NULL AND expires_at>now() FOR UPDATE`, *a.SessionID, c.AccountID, a.SessionTokenHash).Scan(&sessionVersion, &mfaVersion, &verified, &stepup, &expiresAt)
	if err != nil {
		return err
	}
	if sessionVersion != version || !expiresAt.After(time.Now()) {
		return ErrConflict
	}
	if account.Role == "admin" && (factorVersion == nil || mfaVersion == nil || *factorVersion != *mfaVersion || verified == nil || !verified.After(time.Now()) || stepup == nil || !stepup.After(time.Now())) {
		return ErrConflict
	}
	return nil
}
func rotateOAuthSession(ctx context.Context, tx pgx.Tx, a OAuthAttempt, c Credential, newHash []byte) error {
	if _, err := tx.Exec(ctx, `UPDATE auth.credentials SET version=version+1 WHERE account_id=$1`, c.AccountID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE auth.sessions SET credential_version=credential_version+1,token_hash=$2 WHERE id=$1 AND revoked_at IS NULL AND expires_at>clock_timestamp()`, *a.SessionID, newHash)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE auth.sessions SET revoked_at=now() WHERE account_id=$1 AND id<>$2 AND revoked_at IS NULL`, c.AccountID, *a.SessionID)
	return err
}
func (r *Repository) UnlinkOAuth(ctx context.Context, a OAuthAttempt, c Credential, provider, id string, newHash []byte, lock OAuthAccountLock, audit OAuthAudit) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = oauthLinkFence(ctx, tx, a, c, lock); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `DELETE FROM auth.oauth_identities WHERE id=$1 AND account_id=$2 AND provider=$3`, id, c.AccountID, provider)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	if err = rotateOAuthSession(ctx, tx, a, c, newHash); err != nil {
		return err
	}
	if audit != nil {
		if err = audit(ctx, tx, c.AccountID, *a.SessionID, id, provider, "oauth.unlinked"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
