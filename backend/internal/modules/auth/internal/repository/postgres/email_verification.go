package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrVerificationRateLimited = errors.New("email verification requested too recently")

func (r *Repository) InsertEmailVerification(ctx context.Context, accountID, email string, tokenHash []byte, expires time.Time, payload []byte, jobID string) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var active bool
	if err = tx.QueryRow(ctx, `SELECT status='active' FROM accounts.accounts WHERE id=$1 FOR SHARE`, accountID).Scan(&active); err != nil {
		return false, err
	}
	if !active {
		return false, ErrInvalid
	}
	var current string
	var verified *time.Time
	if err = tx.QueryRow(ctx, `SELECT email,email_verified_at FROM auth.credentials WHERE account_id=$1 FOR UPDATE`, accountID).Scan(&current, &verified); err != nil {
		return false, err
	}
	if current != email {
		return false, ErrInvalid
	}
	if verified != nil {
		return false, tx.Commit(ctx)
	}
	var recent bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth.email_verification_tokens WHERE account_id=$1 AND created_at>now()-interval '60 seconds')`, accountID).Scan(&recent); err != nil {
		return false, err
	}
	if recent {
		return false, ErrVerificationRateLimited
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.email_verification_tokens SET consumed_at=now() WHERE account_id=$1 AND consumed_at IS NULL`, accountID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.mail_jobs SET status='expired',finished_at=now(),payload=decode('','hex'),lease_token=NULL,lease_until=NULL,last_error='token_replaced'
 WHERE purpose='email_verification' AND token_hash IN (SELECT token_hash FROM auth.email_verification_tokens WHERE account_id=$1) AND status IN ('pending','sending')`, accountID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth.email_verification_tokens(token_hash,account_id,email,expires_at) VALUES($1,$2,$3,$4)`, tokenHash, accountID, email, expires); err != nil {
		return false, err
	}
	if len(payload) > 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO auth.mail_jobs(id,purpose,token_hash,payload,expires_at) VALUES($1,'email_verification',$2,$3,$4)`, jobID, tokenHash, payload, expires); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

func (r *Repository) ConfirmEmailVerification(ctx context.Context, accountID string, tokenHash []byte) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var active bool
	if err = tx.QueryRow(ctx, `SELECT status='active' FROM accounts.accounts WHERE id=$1 FOR SHARE`, accountID).Scan(&active); err != nil {
		return err
	}
	if !active {
		return ErrInvalid
	}
	var current string
	var verified *time.Time
	if err = tx.QueryRow(ctx, `SELECT email,email_verified_at FROM auth.credentials WHERE account_id=$1 FOR UPDATE`, accountID).Scan(&current, &verified); err != nil {
		return err
	}
	var email string
	var expires time.Time
	var consumed *time.Time
	err = tx.QueryRow(ctx, `SELECT email,expires_at,consumed_at FROM auth.email_verification_tokens WHERE token_hash=$1 AND account_id=$2 FOR UPDATE`, tokenHash, accountID).Scan(&email, &expires, &consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	if email != current || !expires.After(time.Now()) || consumed != nil && verified == nil {
		return ErrInvalid
	}
	if consumed != nil {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.credentials SET email_verified_at=COALESCE(email_verified_at,now()) WHERE account_id=$1`, accountID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.email_verification_tokens SET consumed_at=now() WHERE account_id=$1 AND consumed_at IS NULL`, accountID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.mail_jobs SET status='expired',finished_at=now(),payload=decode('','hex'),lease_token=NULL,lease_until=NULL,last_error='email_verified'
 WHERE purpose='email_verification' AND token_hash IN (SELECT token_hash FROM auth.email_verification_tokens WHERE account_id=$1) AND status IN ('pending','sending')`, accountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
