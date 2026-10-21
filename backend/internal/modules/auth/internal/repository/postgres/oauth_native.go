package postgres

import (
	"context"
	"time"
)

type NativeOAuthAttempt struct {
	ID, Provider, Mode, PKCEChallenge string
	StateHash                         []byte
	AccountID, SessionID              *string
	CredentialVersion                 *int64
	SessionTokenHash                  []byte
	ExpiresAt                         time.Time
	ConsumedAt                        *time.Time
}

func (a NativeOAuthAttempt) WebFence() OAuthAttempt {
	return OAuthAttempt{Provider: a.Provider, Mode: a.Mode, AccountID: a.AccountID,
		SessionID: a.SessionID, CredentialVersion: a.CredentialVersion, SessionTokenHash: a.SessionTokenHash}
}

func (r *Repository) InsertNativeOAuthAttempt(ctx context.Context, a NativeOAuthAttempt, credential Credential, lock OAuthAccountLock) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if a.Mode == "link" {
		if err = oauthLinkFence(ctx, tx, a.WebFence(), credential, lock); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO auth.native_oauth_attempts
		(id,provider,mode,state_hash,pkce_challenge,account_id,session_id,credential_version,session_token_hash,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, a.ID, a.Provider, a.Mode, a.StateHash,
		a.PKCEChallenge, a.AccountID, a.SessionID, a.CredentialVersion, a.SessionTokenHash, a.ExpiresAt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) NativeOAuthAttemptByState(ctx context.Context, provider string, stateHash []byte) (NativeOAuthAttempt, error) {
	var a NativeOAuthAttempt
	err := r.pool.QueryRow(ctx, `SELECT id::text,provider,mode,pkce_challenge,expires_at,consumed_at
		FROM auth.native_oauth_attempts WHERE provider=$1 AND state_hash=$2`,
		provider, stateHash).Scan(&a.ID, &a.Provider, &a.Mode, &a.PKCEChallenge, &a.ExpiresAt, &a.ConsumedAt)
	return a, err
}

func (r *Repository) ConsumeNativeOAuthAttempt(ctx context.Context, id string, stateHash []byte, challenge string) (NativeOAuthAttempt, error) {
	var a NativeOAuthAttempt
	err := r.pool.QueryRow(ctx, `UPDATE auth.native_oauth_attempts SET consumed_at=clock_timestamp()
		WHERE id=$1 AND state_hash=$2 AND pkce_challenge=$3 AND consumed_at IS NULL AND expires_at>clock_timestamp()
		RETURNING id::text,provider,mode,pkce_challenge,account_id::text,session_id::text,
		credential_version,session_token_hash,expires_at`, id, stateHash, challenge).
		Scan(&a.ID, &a.Provider, &a.Mode, &a.PKCEChallenge, &a.AccountID, &a.SessionID,
			&a.CredentialVersion, &a.SessionTokenHash, &a.ExpiresAt)
	return a, err
}
