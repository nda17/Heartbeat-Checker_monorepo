package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type EmailCodeChallenge struct {
	ID, Purpose, Email, AccountID, Name, PasswordHash string
	BrowserHash, CodeHash                             []byte
	DestinationID                                     *string
	DestinationVersion                                int64
	Deliverable                                       bool
	Attempts                                          int
	ExpiresAt, ResendAvailableAt, DBNow               time.Time
	ConsumedAt                                        *time.Time
}

func (r *Repository) Begin(ctx context.Context) (pgx.Tx, error) { return r.pool.Begin(ctx) }

func InsertEmailCodeTx(ctx context.Context, tx pgx.Tx, c EmailCodeChallenge, payload []byte, jobID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO auth.email_code_challenges(id,purpose,email,account_id,name,password_hash,browser_hash,destination_id,destination_version,code_hash,deliverable,expires_at,resend_available_at)
        VALUES($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,''),NULLIF($6,''),$7,$8,$9,$10,$11,$12,$13)`, c.ID, c.Purpose, c.Email, c.AccountID, c.Name, c.PasswordHash, c.BrowserHash, c.DestinationID, c.DestinationVersion, c.CodeHash, c.Deliverable, c.ExpiresAt, c.ResendAvailableAt)
	if err != nil {
		return err
	}
	return InsertEmailCodeMailTx(ctx, tx, c, payload, jobID)
}

func InsertEmailCodeMailTx(ctx context.Context, tx pgx.Tx, c EmailCodeChallenge, payload []byte, jobID string) error {
	if !c.Deliverable {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO auth.mail_jobs(id,purpose,token_hash,payload,expires_at) VALUES($1,'email_code',$2,$3,$4)`, jobID, c.CodeHash, payload, c.ExpiresAt)
	return err
}

func EmailCodeTx(ctx context.Context, tx pgx.Tx, id, purpose, accountID string) (EmailCodeChallenge, error) {
	var c EmailCodeChallenge
	err := tx.QueryRow(ctx, `SELECT id::text,purpose,email,COALESCE(account_id::text,''),COALESCE(name,''),COALESCE(password_hash,''),browser_hash,code_hash,destination_id::text,destination_version,deliverable,failed_attempts,expires_at,resend_available_at,consumed_at
      FROM auth.email_code_challenges WHERE id=$1 AND purpose=$2 AND COALESCE(account_id::text,'')=$3 FOR UPDATE`, id, purpose, accountID).Scan(&c.ID, &c.Purpose, &c.Email, &c.AccountID, &c.Name, &c.PasswordHash, &c.BrowserHash, &c.CodeHash, &c.DestinationID, &c.DestinationVersion, &c.Deliverable, &c.Attempts, &c.ExpiresAt, &c.ResendAvailableAt, &c.ConsumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrInvalid
	}
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&c.DBNow)
	}
	return c, err
}

func (r *Repository) CurrentNotificationEmail(ctx context.Context, accountID string) (EmailCodeChallenge, error) {
	var c EmailCodeChallenge
	err := r.pool.QueryRow(ctx, `SELECT id::text,email,expires_at,resend_available_at,clock_timestamp() FROM auth.email_code_challenges WHERE account_id=$1 AND purpose='notification_email' AND consumed_at IS NULL AND expires_at>now() AND failed_attempts<5 ORDER BY created_at DESC,id DESC LIMIT 1`, accountID).Scan(&c.ID, &c.Email, &c.ExpiresAt, &c.ResendAvailableAt, &c.DBNow)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrInvalid
	}
	return c, err
}

func CancelEmailCodeMailTx(ctx context.Context, tx pgx.Tx, hash []byte) error {
	_, err := tx.Exec(ctx, `UPDATE auth.mail_jobs SET status='expired',finished_at=now(),payload=decode('','hex'),lease_token=NULL,lease_until=NULL,last_error='code_replaced' WHERE purpose='email_code' AND token_hash=$1 AND status IN ('pending','sending')`, hash)
	return err
}

// EmailCode reads a preflight snapshot without reserving a pool connection or
// taking row locks. Mutations always recheck it under EmailCodeTx afterward.
func (r *Repository) EmailCode(ctx context.Context, id, purpose, accountID string) (EmailCodeChallenge, error) {
	var c EmailCodeChallenge
	err := r.pool.QueryRow(ctx, `SELECT id::text,purpose,email,COALESCE(account_id::text,''),COALESCE(name,''),COALESCE(password_hash,''),browser_hash,code_hash,destination_id::text,destination_version,deliverable,failed_attempts,expires_at,resend_available_at,consumed_at,clock_timestamp()
      FROM auth.email_code_challenges WHERE id=$1 AND purpose=$2 AND COALESCE(account_id::text,'')=$3`, id, purpose, accountID).Scan(&c.ID, &c.Purpose, &c.Email, &c.AccountID, &c.Name, &c.PasswordHash, &c.BrowserHash, &c.CodeHash, &c.DestinationID, &c.DestinationVersion, &c.Deliverable, &c.Attempts, &c.ExpiresAt, &c.ResendAvailableAt, &c.ConsumedAt, &c.DBNow)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrInvalid
	}
	return c, err
}
