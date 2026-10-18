package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
)

var ErrInvalid = errors.New("invalid or expired credential")
var ErrConflict = errors.New("credential changed concurrently")
var ErrAlreadyExists = errors.New("account email already exists")

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

type Credential struct {
	AccountID    string
	Email        string
	PasswordHash string
	Version      int64
}
type Challenge struct {
	ID           string
	Email        string
	Name         string
	PasswordHash string
	ExpiresAt    time.Time
	Attempts     int
}
type Session struct {
	ID         string
	AccountID  string
	Email      string
	IP         string
	UserAgent  string
	Version    int64
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
}

func (r *Repository) Allow(ctx context.Context, scope, key string, max int, window time.Duration) (bool, error) {
	if key == "" {
		return false, ErrInvalid
	}
	var count int
	err := r.pool.QueryRow(ctx, `INSERT INTO auth.rate_limits (scope,key_hash,count,window_end)
		VALUES ($1,$2,1,now()+$3 * interval '1 second')
		ON CONFLICT (scope,key_hash) DO UPDATE SET
		count=CASE WHEN auth.rate_limits.window_end<=now() THEN 1 ELSE auth.rate_limits.count+1 END,
		window_end=CASE WHEN auth.rate_limits.window_end<=now() THEN now()+$3 * interval '1 second' ELSE auth.rate_limits.window_end END
		RETURNING count`, scope, security.Digest(key), int64(window.Seconds())).Scan(&count)
	return count <= max, err
}

func (r *Repository) EmailExists(ctx context.Context, email string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth.credentials WHERE email=$1)`, email).Scan(&exists)
	return exists, err
}

func (r *Repository) InsertChallenge(ctx context.Context, c Challenge, tokenHash []byte) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO auth.registration_challenges (id,email,name,password_hash,token_hash,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6)`, c.ID, c.Email, c.Name, c.PasswordHash, tokenHash, c.ExpiresAt)
	return err
}

func (r *Repository) Challenge(ctx context.Context, tokenHash []byte) (Challenge, error) {
	var c Challenge
	err := r.pool.QueryRow(ctx, `SELECT id,email,name,password_hash,expires_at,failed_attempts FROM auth.registration_challenges
		WHERE token_hash=$1 AND consumed_at IS NULL`, tokenHash).Scan(&c.ID, &c.Email, &c.Name, &c.PasswordHash, &c.ExpiresAt, &c.Attempts)
	return c, err
}

// CompleteRegistration owns the short transaction shared with accounts.Create.
// A wrong password is counted and committed; a valid token creates exactly one account.
func (r *Repository) CompleteRegistration(ctx context.Context, c Challenge, passwordOK bool, createAccount func(context.Context, pgx.Tx, string) (string, error)) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var currentHash string
	var expires time.Time
	var attempts int
	err = tx.QueryRow(ctx, `SELECT password_hash,expires_at,failed_attempts FROM auth.registration_challenges
		WHERE id=$1 AND consumed_at IS NULL FOR UPDATE`, c.ID).Scan(&currentHash, &expires, &attempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalid
		}
		return err
	}
	if currentHash != c.PasswordHash || !expires.After(time.Now()) || attempts >= 5 {
		return ErrInvalid
	}
	if !passwordOK {
		if _, err = tx.Exec(ctx, `UPDATE auth.registration_challenges SET failed_attempts=failed_attempts+1 WHERE id=$1`, c.ID); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return ErrInvalid
	}
	accountID, err := createAccount(ctx, tx, c.Name)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO auth.credentials (account_id,email,password_hash) VALUES ($1,$2,$3)`, accountID, c.Email, c.PasswordHash)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrInvalid
		}
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE auth.registration_challenges SET consumed_at=now() WHERE id=$1`, c.ID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// BootstrapLocal is create-only: an existing email rolls back the newly created
// account, without changing existing credentials, sessions or entitlements.
func (r *Repository) BootstrapLocal(ctx context.Context, email, name, hash string,
	createAccount func(context.Context, pgx.Tx, string) (string, error),
	grant func(context.Context, pgx.Tx, string) error,
) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	accountID, err := createAccount(ctx, tx, name)
	if err != nil {
		return "", err
	}
	result, err := tx.Exec(ctx, `INSERT INTO auth.credentials (account_id,email,password_hash)
		VALUES ($1,$2,$3) ON CONFLICT (email) DO NOTHING`, accountID, email, hash)
	if err != nil {
		return "", err
	}
	if result.RowsAffected() != 1 {
		return "", ErrAlreadyExists
	}
	if err = grant(ctx, tx, accountID); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return accountID, nil
}

func (r *Repository) CredentialByEmail(ctx context.Context, email string) (Credential, error) {
	var c Credential
	err := r.pool.QueryRow(ctx, `SELECT account_id,email,password_hash,version FROM auth.credentials WHERE email=$1`, email).Scan(&c.AccountID, &c.Email, &c.PasswordHash, &c.Version)
	return c, err
}

func (r *Repository) CredentialByAccount(ctx context.Context, accountID string) (Credential, error) {
	var c Credential
	err := r.pool.QueryRow(ctx, `SELECT account_id,email,password_hash,version FROM auth.credentials WHERE account_id=$1`, accountID).Scan(&c.AccountID, &c.Email, &c.PasswordHash, &c.Version)
	return c, err
}

// LoginSession serializes session creation with password reset/change on credentials.
func (r *Repository) LoginSession(ctx context.Context, c Credential, sessionID string, tokenHash []byte, expires time.Time, ip, agent string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var hash string
	var version int64
	err = tx.QueryRow(ctx, `SELECT password_hash,version FROM auth.credentials WHERE account_id=$1 FOR UPDATE`, c.AccountID).Scan(&hash, &version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	if hash != c.PasswordHash || version != c.Version {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO auth.sessions (id,account_id,token_hash,credential_version,expires_at,ip,user_agent)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,'')::inet,$7)`, sessionID, c.AccountID, tokenHash, version, expires, ip, agent)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) SessionByToken(ctx context.Context, tokenHash []byte) (Session, error) {
	var s Session
	err := r.pool.QueryRow(ctx, `SELECT s.id,s.account_id,c.email,s.credential_version,s.created_at,s.last_used_at,s.expires_at
		FROM auth.sessions s JOIN auth.credentials c ON c.account_id=s.account_id AND c.version=s.credential_version
		WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now()`, tokenHash).Scan(&s.ID, &s.AccountID, &s.Email, &s.Version, &s.CreatedAt, &s.LastUsedAt, &s.ExpiresAt)
	return s, err
}

func (r *Repository) TouchSession(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `UPDATE auth.sessions SET last_used_at=now()
		WHERE id=$1 AND revoked_at IS NULL AND expires_at>now() AND last_used_at<now()-interval '1 minute'`, id)
	return err
}

func (r *Repository) Sessions(ctx context.Context, accountID string) ([]Session, error) {
	rows, err := r.pool.Query(ctx, `SELECT id,created_at,last_used_at,expires_at,COALESCE(ip::text,''),user_agent FROM auth.sessions
		WHERE account_id=$1 AND revoked_at IS NULL AND expires_at>now() ORDER BY created_at DESC LIMIT 100`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Session, 0)
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.CreatedAt, &s.LastUsedAt, &s.ExpiresAt, &s.IP, &s.UserAgent); err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	return items, rows.Err()
}

func (r *Repository) RevokeSession(ctx context.Context, accountID, sessionID string) (bool, error) {
	cmd, err := r.pool.Exec(ctx, `UPDATE auth.sessions SET revoked_at=now() WHERE id=$1 AND account_id=$2 AND revoked_at IS NULL`, sessionID, accountID)
	return cmd.RowsAffected() != 0, err
}

func (r *Repository) RevokeAllSessions(ctx context.Context, accountID string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var version int64
	if err = tx.QueryRow(ctx, `SELECT version FROM auth.credentials WHERE account_id=$1 FOR UPDATE`, accountID).Scan(&version); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.credentials SET version=version+1 WHERE account_id=$1`, accountID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.sessions SET revoked_at=now() WHERE account_id=$1 AND revoked_at IS NULL`, accountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) InsertReset(ctx context.Context, accountID string, version int64, tokenHash []byte, expires time.Time) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO auth.password_reset_tokens (token_hash,account_id,credential_version,expires_at)
		VALUES ($1,$2,$3,$4)`, tokenHash, accountID, version, expires)
	return err
}

func (r *Repository) ResetAccount(ctx context.Context, tokenHash []byte) (string, error) {
	var accountID string
	err := r.pool.QueryRow(ctx, `SELECT account_id FROM auth.password_reset_tokens WHERE token_hash=$1`, tokenHash).Scan(&accountID)
	return accountID, err
}

// CompleteReset locks credentials first, as LoginSession and ChangePassword do.
func (r *Repository) CompleteReset(ctx context.Context, accountID string, tokenHash []byte, newHash string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var version int64
	err = tx.QueryRow(ctx, `SELECT version FROM auth.credentials WHERE account_id=$1 FOR UPDATE`, accountID).Scan(&version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalid
		}
		return err
	}
	var tokenVersion int64
	var expires time.Time
	var consumed *time.Time
	err = tx.QueryRow(ctx, `SELECT credential_version,expires_at,consumed_at FROM auth.password_reset_tokens WHERE token_hash=$1 FOR UPDATE`, tokenHash).Scan(&tokenVersion, &expires, &consumed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalid
		}
		return err
	}
	if consumed != nil || tokenVersion != version || !expires.After(time.Now()) {
		return ErrInvalid
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.credentials SET password_hash=$2,version=version+1,password_changed_at=now() WHERE account_id=$1`, accountID, newHash); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.password_reset_tokens SET consumed_at=now() WHERE account_id=$1 AND consumed_at IS NULL`, accountID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.sessions SET revoked_at=now() WHERE account_id=$1 AND revoked_at IS NULL`, accountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) ChangePassword(ctx context.Context, c Credential, newHash string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var hash string
	var version int64
	err = tx.QueryRow(ctx, `SELECT password_hash,version FROM auth.credentials WHERE account_id=$1 FOR UPDATE`, c.AccountID).Scan(&hash, &version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	if hash != c.PasswordHash || version != c.Version {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.credentials SET password_hash=$2,version=version+1,password_changed_at=now() WHERE account_id=$1`, c.AccountID, newHash); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.password_reset_tokens SET consumed_at=now() WHERE account_id=$1 AND consumed_at IS NULL`, c.AccountID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.sessions SET revoked_at=now() WHERE account_id=$1 AND revoked_at IS NULL`, c.AccountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Cleanup removes only unusable auth data, in bounded batches. Live sessions and
// credentials remain untouched, including when cleanup races login or reset.
func Cleanup(ctx context.Context, tx pgx.Tx) error {
	for _, query := range []string{
		`UPDATE auth.mail_jobs SET status='expired',finished_at=now(),payload=decode('','hex'),lease_token=NULL,lease_until=NULL,last_error='token_expired'
			WHERE id IN (SELECT id FROM auth.mail_jobs WHERE status IN ('pending','sending') AND (expires_at<=now()
			OR (purpose='registration' AND NOT EXISTS (SELECT 1 FROM auth.registration_challenges c WHERE c.token_hash=auth.mail_jobs.token_hash AND c.consumed_at IS NULL AND c.expires_at>now()))
			OR (purpose='reset' AND NOT EXISTS (SELECT 1 FROM auth.password_reset_tokens t JOIN auth.credentials c ON c.account_id=t.account_id WHERE t.token_hash=auth.mail_jobs.token_hash AND t.consumed_at IS NULL AND t.expires_at>now() AND t.credential_version=c.version)))
			ORDER BY expires_at LIMIT 500 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM auth.mail_jobs WHERE id IN (SELECT id FROM auth.mail_jobs WHERE status IN ('sent','failed','expired') AND finished_at<now()-interval '30 days' ORDER BY finished_at LIMIT 500 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM auth.sessions WHERE id IN (SELECT id FROM auth.sessions WHERE expires_at<=now() OR revoked_at IS NOT NULL ORDER BY expires_at LIMIT 500 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM auth.registration_challenges WHERE id IN (SELECT id FROM auth.registration_challenges WHERE expires_at<=now() OR consumed_at IS NOT NULL ORDER BY expires_at LIMIT 500 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM auth.password_reset_tokens WHERE token_hash IN (SELECT token_hash FROM auth.password_reset_tokens WHERE expires_at<=now() OR consumed_at IS NOT NULL ORDER BY expires_at LIMIT 500 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM auth.rate_limits WHERE (scope,key_hash) IN (SELECT scope,key_hash FROM auth.rate_limits WHERE window_end<=now() ORDER BY window_end LIMIT 500 FOR UPDATE SKIP LOCKED)`,
	} {
		if _, err := tx.Exec(ctx, query); err != nil {
			return err
		}
	}
	return nil
}
