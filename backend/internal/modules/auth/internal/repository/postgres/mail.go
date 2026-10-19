package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
)

type MailJob struct {
	ID         string
	Purpose    string
	Payload    []byte
	LeaseToken string
	ExpiresAt  time.Time
	Attempts   int
}

func (r *Repository) InsertChallengeWithMail(ctx context.Context, c Challenge, tokenHash, payload []byte, jobID string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO auth.registration_challenges (id,email,name,password_hash,token_hash,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6)`, c.ID, c.Email, c.Name, c.PasswordHash, tokenHash, c.ExpiresAt); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth.mail_jobs(id,purpose,token_hash,payload,expires_at) VALUES ($1,'registration',$2,$3,$4)`, jobID, tokenHash, payload, c.ExpiresAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) InsertResetWithMail(ctx context.Context, accountID string, version int64, tokenHash []byte, expires time.Time, payload []byte, jobID string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = insertResetTx(ctx, tx, accountID, version, tokenHash, expires); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth.mail_jobs(id,purpose,token_hash,payload,expires_at) VALUES ($1,'reset',$2,$3,$4)`, jobID, tokenHash, payload, expires); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ClaimMail holds row locks only while assigning a lease. Sending happens afterward.
func (r *Repository) ClaimMail(ctx context.Context) (*MailJob, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	lease, err := security.ID()
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.mail_jobs SET status='failed',finished_at=now(),payload=decode('','hex'),
		lease_token=NULL,lease_until=NULL,last_error='attempt_limit_exceeded'
		WHERE attempts>=8 AND (status='pending' OR (status='sending' AND lease_until<now()))`); err != nil {
		return nil, err
	}
	job := &MailJob{LeaseToken: lease}
	err = tx.QueryRow(ctx, `SELECT id,purpose,payload,expires_at,attempts FROM auth.mail_jobs
		WHERE expires_at>now() AND next_attempt_at<=now() AND attempts<8
		AND (status='pending' OR (status='sending' AND lease_until<now()))
		AND ((purpose='registration' AND EXISTS (SELECT 1 FROM auth.registration_challenges c WHERE c.token_hash=auth.mail_jobs.token_hash AND c.consumed_at IS NULL AND c.expires_at>now()))
		OR (purpose='reset' AND EXISTS (SELECT 1 FROM auth.password_reset_tokens t JOIN auth.credentials c ON c.account_id=t.account_id
			JOIN accounts.accounts a ON a.id=t.account_id AND a.status='active'
			WHERE t.token_hash=auth.mail_jobs.token_hash AND t.consumed_at IS NULL AND t.expires_at>now() AND t.credential_version=c.version)))
		ORDER BY next_attempt_at,created_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&job.ID, &job.Purpose, &job.Payload, &job.ExpiresAt, &job.Attempts)
	if err == pgx.ErrNoRows {
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth.mail_jobs SET status='sending',lease_token=$2,lease_until=now()+interval '30 seconds',attempts=attempts+1 WHERE id=$1`, job.ID, lease); err != nil {
		return nil, err
	}
	job.Attempts++
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return job, nil
}

func (r *Repository) CompleteMail(ctx context.Context, job MailJob, sent, permanent bool) error {
	if sent {
		_, err := r.pool.Exec(ctx, `UPDATE auth.mail_jobs SET status='sent',finished_at=now(),payload=decode('','hex'),lease_token=NULL,lease_until=NULL,last_error=NULL
			WHERE id=$1 AND status='sending' AND lease_token=$2`, job.ID, job.LeaseToken)
		return err
	}
	backoff := time.Duration(5) * time.Second
	for i := 1; i < job.Attempts && backoff < 30*time.Minute; i++ {
		backoff *= 3
	}
	if backoff > 30*time.Minute {
		backoff = 30 * time.Minute
	}
	_, err := r.pool.Exec(ctx, `UPDATE auth.mail_jobs SET
		status=CASE WHEN $4 OR attempts>=8 OR expires_at<=now()+$3::interval THEN 'failed' ELSE 'pending' END,
		finished_at=CASE WHEN $4 OR attempts>=8 OR expires_at<=now()+$3::interval THEN now() ELSE NULL END,
		payload=CASE WHEN $4 OR attempts>=8 OR expires_at<=now()+$3::interval THEN decode('','hex') ELSE payload END,
		next_attempt_at=now()+$3::interval,lease_token=NULL,lease_until=NULL,last_error='smtp_send_failed'
		WHERE id=$1 AND status='sending' AND lease_token=$2`, job.ID, job.LeaseToken, backoff.String(), permanent)
	return err
}
