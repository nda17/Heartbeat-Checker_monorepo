package notifications

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

func (p *Push) CreatePairing(ctx context.Context, bearer, ip string, in PushPairingInput) (PushPairingResult, error) {
	if err := p.enabled(); err != nil {
		return PushPairingResult{}, err
	}
	in.DeviceLabel = strings.TrimSpace(in.DeviceLabel)
	if !pushChallenge(in.CodeChallenge) || !pushChallenge(in.ClientState) || !pushLabel(in.DeviceLabel) || !pushAppVersion(in.AppVersion) || ip == "" {
		return PushPairingResult{}, ErrPushInvalid
	}
	var install pushInstallation
	var credential string
	var err error
	if bearer != "" {
		install, err = p.authenticate(ctx, bearer)
		if err != nil {
			return PushPairingResult{}, err
		}
	} else {
		install.ID, err = outbox.ID()
		if err != nil {
			return PushPairingResult{}, err
		}
		secret, e := pushSecret()
		if e != nil {
			return PushPairingResult{}, e
		}
		credential = install.ID + "." + secret
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return PushPairingResult{}, err
	}
	defer tx.Rollback(ctx)
	// The bounded bootstrap is serialized before allocating a new installation.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7349281728)`); err != nil {
		return PushPairingResult{}, err
	}
	fingerprint := p.fingerprint("pair-ip:" + ip)
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM notifications.push_pairings WHERE request_fingerprint=$1 AND created_at>now()-interval '10 minutes'`, fingerprint).Scan(&count); err != nil {
		return PushPairingResult{}, err
	}
	if count >= 10 {
		return PushPairingResult{}, ErrPushRateLimited
	}
	if credential != "" {
		hash := sha256.Sum256([]byte(credential))
		_, err = tx.Exec(ctx, `INSERT INTO notifications.push_installations(id,credential_hash,device_label,app_version) VALUES($1,$2,$3,$4)`, install.ID, hash[:], in.DeviceLabel, in.AppVersion)
	} else {
		var status string
		err = tx.QueryRow(ctx, `SELECT status FROM notifications.push_installations WHERE id=$1 FOR UPDATE`, install.ID).Scan(&status)
		if err == nil && status == "revoked" {
			return PushPairingResult{}, ErrPushUnauthorized
		}
	}
	if err != nil {
		return PushPairingResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE notifications.push_pairings SET expires_at=now() WHERE installation_id=$1 AND consumed_at IS NULL AND expires_at>now()`, install.ID); err != nil {
		return PushPairingResult{}, err
	}
	id, err := outbox.ID()
	if err != nil {
		return PushPairingResult{}, err
	}
	var expiry time.Time
	err = tx.QueryRow(ctx, `INSERT INTO notifications.push_pairings(id,installation_id,code_challenge,client_state,request_fingerprint,expires_at) VALUES($1,$2,$3,$4,$5,now()+interval '5 minutes') RETURNING expires_at`, id, install.ID, in.CodeChallenge, in.ClientState, fingerprint).Scan(&expiry)
	if err != nil {
		return PushPairingResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PushPairingResult{}, err
	}
	return PushPairingResult{Pairing: PushPairing{ID: id, ExpiresAt: expiry, ConfirmationURL: p.cfg.Origin + "/app/settings?pushPair=" + id, Status: "pending", DeviceLabel: in.DeviceLabel, AppVersion: in.AppVersion}, Installation: PushPairingInstallation{ID: install.ID, Credential: credential}}, nil
}

func (p *Push) Pairing(ctx context.Context, accountID, id string) (PushPairing, error) {
	if err := p.enabled(); err != nil {
		return PushPairing{}, err
	}
	if err := pushContextID(id); err != nil {
		return PushPairing{}, err
	}
	var result PushPairing
	err := p.pool.QueryRow(ctx, `UPDATE notifications.push_pairings q SET read_count=read_count+1 FROM notifications.push_installations i WHERE q.id=$1 AND i.id=q.installation_id AND i.status<>'revoked' AND q.expires_at>now() AND q.consumed_at IS NULL AND (q.account_id IS NULL OR q.account_id=$2) AND q.read_count<150 RETURNING q.id::text,q.expires_at,i.device_label,i.app_version,CASE WHEN q.account_id IS NULL THEN 'pending' ELSE 'confirmed' END`, id, accountID).Scan(&result.ID, &result.ExpiresAt, &result.DeviceLabel, &result.AppVersion, &result.Status)
	return result, pushError(err)
}

func (p *Push) ConfirmPairing(ctx context.Context, accountID, sessionID, id string) (PushPairingConfirmation, error) {
	if err := p.enabled(); err != nil {
		return PushPairingConfirmation{}, err
	}
	if err := pushContextID(id); err != nil {
		return PushPairingConfirmation{}, err
	}
	var installationID string
	if err := p.pool.QueryRow(ctx, `SELECT installation_id::text FROM notifications.push_pairings WHERE id=$1 AND expires_at>now() AND consumed_at IS NULL AND (account_id IS NULL OR account_id=$2)`, id, accountID).Scan(&installationID); err != nil {
		return PushPairingConfirmation{}, pushError(err)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return PushPairingConfirmation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = p.entitlement.Lock(ctx, tx, accountID); err != nil {
		return PushPairingConfirmation{}, err
	}
	if _, err = p.sessionLease(ctx, tx, accountID, sessionID); err != nil {
		return PushPairingConfirmation{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM notifications.push_installations WHERE account_id=$1 AND status='ready' AND id<>$2`, accountID, installationID).Scan(&count); err != nil {
		return PushPairingConfirmation{}, err
	}
	if count >= 10 {
		return PushPairingConfirmation{}, ErrPushRateLimited
	}
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM notifications.push_installations WHERE id=$1 FOR UPDATE`, installationID).Scan(&status); err != nil {
		return PushPairingConfirmation{}, pushError(err)
	}
	if status == "revoked" {
		return PushPairingConfirmation{}, ErrPushNotFound
	}
	code, err := pushSecret()
	if err != nil {
		return PushPairingConfirmation{}, err
	}
	hash := sha256.Sum256([]byte(code))
	result := PushPairingConfirmation{PairingID: id, CompletionCode: code}
	err = tx.QueryRow(ctx, `UPDATE notifications.push_pairings SET account_id=$2,session_id=$3,completion_code_hash=$4 WHERE id=$1 AND expires_at>now() AND consumed_at IS NULL AND account_id IS NULL RETURNING client_state,expires_at`, id, accountID, sessionID, hash[:]).Scan(&result.ClientState, &result.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PushPairingConfirmation{}, ErrPushConflict
		}
		return PushPairingConfirmation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PushPairingConfirmation{}, err
	}
	return result, nil
}

func (p *Push) ExchangePairing(ctx context.Context, bearer, id string, in PushExchangeInput) (PushInstallationStatus, error) {
	i, err := p.authenticate(ctx, bearer)
	if err != nil {
		return PushInstallationStatus{}, err
	}
	if !messengerValidID(id) || !pushChallenge(in.CodeVerifier) || !pushChallenge(in.CompletionCode) || !p.providerEnabled(in.Provider) || !pushToken(in.Token) || in.Permission != "granted" || !pushAppVersion(in.AppVersion) {
		return PushInstallationStatus{}, ErrPushInvalid
	}
	var accountID, sessionID string
	err = p.pool.QueryRow(ctx, `SELECT COALESCE(account_id::text,''),COALESCE(session_id::text,'') FROM notifications.push_pairings WHERE id=$1 AND installation_id=$2`, id, i.ID).Scan(&accountID, &sessionID)
	if err != nil {
		return PushInstallationStatus{}, pushError(err)
	}
	if accountID == "" || sessionID == "" {
		return PushInstallationStatus{}, ErrPushConflict
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return PushInstallationStatus{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = p.entitlement.Lock(ctx, tx, accountID); err != nil {
		return PushInstallationStatus{}, err
	}
	expiry, err := p.sessionLease(ctx, tx, accountID, sessionID)
	if err != nil {
		return PushInstallationStatus{}, err
	}
	current, err := scanPushInstallation(tx.QueryRow(ctx, `SELECT `+pushInstallationColumns+` FROM notifications.push_installations WHERE id=$1 AND status<>'revoked' FOR UPDATE`, i.ID))
	if err != nil {
		return PushInstallationStatus{}, pushError(err)
	}
	var challenge string
	var completionHash []byte
	var consumed *time.Time
	var pairExpiry time.Time
	var attempts int
	err = tx.QueryRow(ctx, `SELECT code_challenge,completion_code_hash,consumed_at,expires_at,exchange_attempts FROM notifications.push_pairings WHERE id=$1 AND installation_id=$2 AND account_id=$3 AND session_id=$4 FOR UPDATE`, id, i.ID, accountID, sessionID).Scan(&challenge, &completionHash, &consumed, &pairExpiry, &attempts)
	if err != nil {
		return PushInstallationStatus{}, pushError(err)
	}
	if consumed != nil {
		return PushInstallationStatus{}, ErrPushPairingConsumed
	}
	if !pairExpiry.After(time.Now()) || attempts >= 10 {
		return PushInstallationStatus{}, ErrPushConflict
	}
	digest := sha256.Sum256([]byte(in.CodeVerifier))
	expected := base64.RawURLEncoding.EncodeToString(digest[:])
	codeHash := sha256.Sum256([]byte(in.CompletionCode))
	if subtle.ConstantTimeCompare([]byte(challenge), []byte(expected)) != 1 || subtle.ConstantTimeCompare(completionHash, codeHash[:]) != 1 {
		if _, err = tx.Exec(ctx, `UPDATE notifications.push_pairings SET exchange_attempts=exchange_attempts+1 WHERE id=$1`, id); err != nil {
			return PushInstallationStatus{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return PushInstallationStatus{}, err
		}
		return PushInstallationStatus{}, ErrPushInvalid
	}
	// Account/auth guards above serialize every policy mutation. Enable the
	// inherited default only after the locked proof checks: failed attempts
	// commit their counter without changing the account preference.
	if _, err = tx.Exec(ctx, `INSERT INTO notifications.push_account_settings(account_id,default_enabled) VALUES($1,true) ON CONFLICT(account_id) DO UPDATE SET default_enabled=true`, accountID); err != nil {
		return PushInstallationStatus{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM notifications.push_installations WHERE account_id=$1 AND status='ready' AND id<>$2`, accountID, i.ID).Scan(&count); err != nil {
		return PushInstallationStatus{}, err
	}
	if count >= 10 {
		return PushInstallationStatus{}, ErrPushRateLimited
	}
	changedBinding := current.AccountID != accountID || current.SessionID != sessionID
	if changedBinding {
		if err = p.cancelInstallation(ctx, tx, i.ID, "binding_changed"); err != nil {
			return PushInstallationStatus{}, err
		}
	}
	sealed, err := p.seal(i.ID, "token", []byte(in.Token))
	if err != nil {
		return PushInstallationStatus{}, err
	}
	var generation, version int64
	err = tx.QueryRow(ctx, `UPDATE notifications.push_installations SET account_id=$2,session_id=$3,encrypted_token=$4,token_fingerprint=$5,permission='granted',status='ready',token_valid=true,provider_available=true,binding_generation=binding_generation+CASE WHEN $8 THEN 1 ELSE 0 END,registration_version=registration_version+1,last_pairing_id=$6,app_version=$7,provider=$9,updated_at=now() WHERE id=$1 RETURNING binding_generation,registration_version`, i.ID, accountID, sessionID, sealed, p.fingerprint("registration:"+in.Token), id, in.AppVersion, changedBinding, in.Provider).Scan(&generation, &version)
	if err != nil {
		return PushInstallationStatus{}, pushError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE notifications.push_pairings SET consumed_at=now() WHERE id=$1`, id); err != nil {
		return PushInstallationStatus{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PushInstallationStatus{}, err
	}
	return PushInstallationStatus{ID: i.ID, Provider: in.Provider, DeviceLabel: current.Label, LastSeenAt: time.Now().UTC(), BindingGeneration: generation, RegistrationVersion: version, LeaseExpiresAt: &expiry, Status: "ready", LastPairingID: &id}, nil
}

func (p *Push) UpdateRegistration(ctx context.Context, bearer string, in PushRegistrationInput) (PushInstallationStatus, error) {
	i, err := p.authenticate(ctx, bearer)
	if err != nil {
		return PushInstallationStatus{}, err
	}
	accountID, sourceSession, generation := i.AccountID, i.SessionID, i.Generation
	if in.Provider == "" {
		in.Provider = "rustore"
	}
	if !p.providerEnabled(in.Provider) || in.RegistrationVersion < 1 || (in.Permission != "granted" && in.Permission != "denied") || in.Token != nil && !pushToken(*in.Token) {
		return PushInstallationStatus{}, ErrPushInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return PushInstallationStatus{}, err
	}
	defer tx.Rollback(ctx)
	if i.AccountID != "" {
		if _, err = p.entitlement.Lock(ctx, tx, i.AccountID); err != nil {
			return PushInstallationStatus{}, err
		}
		if _, err = p.sessionLease(ctx, tx, i.AccountID, i.SessionID); err != nil {
			return PushInstallationStatus{}, err
		}
	}
	i, err = scanPushInstallation(tx.QueryRow(ctx, `SELECT `+pushInstallationColumns+` FROM notifications.push_installations WHERE id=$1 AND status<>'revoked' FOR UPDATE`, i.ID))
	if err != nil {
		return PushInstallationStatus{}, pushError(err)
	}
	if i.AccountID != accountID || i.SessionID != sourceSession || i.Generation != generation {
		return PushInstallationStatus{}, ErrPushConflict
	}
	if i.Version != in.RegistrationVersion {
		return PushInstallationStatus{}, ErrPushConflict
	}
	if i.AccountID == "" {
		return PushInstallationStatus{}, ErrPushReconnect
	}
	if in.Provider != i.Provider && in.Token == nil {
		return PushInstallationStatus{}, ErrPushInvalid
	}
	sealed := i.Token
	var fingerprint []byte
	if in.Token != nil {
		sealed, err = p.seal(i.ID, "token", []byte(*in.Token))
		if err != nil {
			return PushInstallationStatus{}, err
		}
		fingerprint = p.fingerprint("registration:" + *in.Token)
	}
	if in.Permission == "granted" && len(sealed) == 0 {
		return PushInstallationStatus{}, ErrPushInvalid
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.push_installations SET permission=$2,encrypted_token=$3,token_fingerprint=COALESCE($4,token_fingerprint),registration_version=registration_version+1,token_valid=CASE WHEN $4::bytea IS NOT NULL THEN true ELSE token_valid END,provider_available=CASE WHEN $4::bytea IS NOT NULL THEN true ELSE provider_available END,provider=$5,updated_at=now() WHERE id=$1`, i.ID, in.Permission, sealed, fingerprint, in.Provider)
	if err != nil {
		return PushInstallationStatus{}, pushError(err)
	}
	if in.Permission == "denied" {
		if err = p.cancelInstallation(ctx, tx, i.ID, "permission_denied"); err != nil {
			return PushInstallationStatus{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return PushInstallationStatus{}, err
	}
	return p.Installation(ctx, bearer)
}

func (p *Push) RevokeInstallation(ctx context.Context, bearer string) error {
	i, err := p.authenticate(ctx, bearer)
	if err != nil {
		return err
	}
	return p.revoke(ctx, i.ID, "")
}
func (p *Push) DisconnectInstallation(ctx context.Context, accountID, id string) error {
	if err := p.enabled(); err != nil {
		return err
	}
	if err := pushContextID(id); err != nil {
		return err
	}
	return p.revoke(ctx, id, accountID)
}
func (p *Push) revoke(ctx context.Context, id, accountID string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Revocation never acquires account/monitor locks after the installation.
	cmd, err := tx.Exec(ctx, `UPDATE notifications.push_installations SET status='revoked',encrypted_token=NULL,token_fingerprint=NULL,binding_generation=binding_generation+1,registration_version=registration_version+1,updated_at=now() WHERE id=$1 AND status<>'revoked' AND ($2='' OR account_id=NULLIF($2,'')::uuid)`, id, accountID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrPushNotFound
	}
	if err = p.cancelInstallation(ctx, tx, id, "installation_revoked"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE notifications.push_pairings SET expires_at=now() WHERE installation_id=$1 AND consumed_at IS NULL`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
