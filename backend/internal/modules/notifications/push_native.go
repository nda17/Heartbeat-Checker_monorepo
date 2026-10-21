package notifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
)

type NativePushBindInput struct {
	InstallationCredential string `json:"installationCredential"`
	Provider               string `json:"provider"`
	Token                  string `json:"token"`
	Permission             string `json:"permission"`
	DeviceLabel            string `json:"deviceLabel"`
	AppVersion             string `json:"appVersion"`
	BindingGeneration      *int64 `json:"bindingGeneration"`
	RegistrationVersion    *int64 `json:"registrationVersion"`
	AllowAccountChange     bool   `json:"allowAccountChange"`
}

type NativePushBindResult struct {
	Installation           PushInstallationStatus `json:"installation"`
	AccountID              string                 `json:"accountId"`
	SessionID              string                 `json:"sessionId"`
	InstallationCredential string                 `json:"installationCredential,omitempty"`
}

func nativeCredentialID(value string) (string, bool) {
	id, secret, ok := strings.Cut(value, ".")
	return id, ok && messengerValidID(id) && pushChallenge(secret)
}

func nativeInstallationStatus(i pushInstallation, expires time.Time) PushInstallationStatus {
	status := PushInstallationStatus{ID: i.ID, Provider: i.Provider, DeviceLabel: i.Label,
		BindingGeneration: i.Generation, RegistrationVersion: i.Version, LastPairingID: pushOptionalID(i.LastPairingID),
		LastSeenAt: i.LastSeenAt, LeaseExpiresAt: &expires, Status: "ready"}
	switch {
	case i.Status == "pending":
		status.Status = "pending"
	case i.Permission != "granted":
		status.Status = "permission_denied"
	case !i.TokenValid:
		status.Status = "invalid_token"
	case !i.ProviderAvailable:
		status.Status = "provider_unavailable"
	}
	return status
}

func (p *Push) BindNative(ctx context.Context, actor authcontracts.Principal, in NativePushBindInput) (NativePushBindResult, error) {
	if err := p.enabled(); err != nil {
		return NativePushBindResult{}, err
	}
	in.DeviceLabel = strings.TrimSpace(in.DeviceLabel)
	if actor.ClientKind != "android" || !messengerValidID(actor.AccountID) || !messengerValidID(actor.SessionID) {
		return NativePushBindResult{}, ErrPushUnauthorized
	}
	if !p.providerEnabled(in.Provider) || !pushToken(in.Token) || in.Permission != "granted" ||
		!pushLabel(in.DeviceLabel) || !pushAppVersion(in.AppVersion) {
		return NativePushBindResult{}, ErrPushInvalid
	}
	credentialID, ok := nativeCredentialID(in.InstallationCredential)
	if !ok {
		return NativePushBindResult{}, ErrPushInvalid
	}
	fresh := in.BindingGeneration == nil && in.RegistrationVersion == nil
	if !fresh && (in.BindingGeneration == nil || in.RegistrationVersion == nil || *in.BindingGeneration < 0 || *in.RegistrationVersion < 1) {
		return NativePushBindResult{}, ErrPushInvalid
	}
	if fresh && in.AllowAccountChange {
		return NativePushBindResult{}, ErrPushInvalid
	}
	requestHash := p.fingerprint("native-bind\x00" + strings.Join([]string{actor.AccountID, actor.SessionID,
		in.Provider, in.Token, in.Permission, in.DeviceLabel, in.AppVersion}, "\x00"))
	credentialHash := sha256.Sum256([]byte(in.InstallationCredential))
	var prior pushInstallation
	var err error
	if !fresh {
		prior, err = p.authenticate(ctx, in.InstallationCredential)
		if err != nil {
			return NativePushBindResult{}, err
		}
		if err = p.rateInstallation(ctx, in.InstallationCredential); err != nil {
			return NativePushBindResult{}, err
		}
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return NativePushBindResult{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = p.entitlement.Lock(ctx, tx, actor.AccountID); err != nil {
		return NativePushBindResult{}, err
	}
	expires, err := p.sessionLease(ctx, tx, actor.AccountID, actor.SessionID)
	if err != nil {
		return NativePushBindResult{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM notifications.push_installations WHERE account_id=$1 AND status='ready' AND id<>$2`, actor.AccountID, credentialID).Scan(&count); err != nil {
		return NativePushBindResult{}, err
	}
	if count >= 10 {
		return NativePushBindResult{}, ErrPushRateLimited
	}
	var sealed []byte
	var status PushInstallationStatus
	if fresh {
		var current pushInstallation
		current, err = scanPushInstallation(tx.QueryRow(ctx, `SELECT `+pushInstallationColumns+`
			FROM notifications.push_installations WHERE id=$1 AND credential_hash=$2 AND status<>'revoked' FOR UPDATE`, credentialID, credentialHash[:]))
		if err == nil {
			var stored []byte
			if err = tx.QueryRow(ctx, `SELECT native_bind_fingerprint FROM notifications.push_installations WHERE id=$1`, credentialID).Scan(&stored); err != nil {
				return NativePushBindResult{}, err
			}
			if current.AccountID != actor.AccountID || current.SessionID != actor.SessionID || !hmac.Equal(stored, requestHash) {
				return NativePushBindResult{}, ErrPushConflict
			}
			status = nativeInstallationStatus(current, expires)
			if err = tx.Commit(ctx); err != nil {
				return NativePushBindResult{}, err
			}
			return NativePushBindResult{Installation: status, AccountID: actor.AccountID, SessionID: actor.SessionID}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return NativePushBindResult{}, err
		}
		sealed, err = p.seal(credentialID, "token", []byte(in.Token))
		if err != nil {
			return NativePushBindResult{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO notifications.push_installations
			(id,credential_hash,account_id,session_id,provider,device_label,app_version,encrypted_token,token_fingerprint,
			registration_version,binding_generation,permission,status,token_valid,provider_available,native_bind_fingerprint)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1,1,'granted','ready',true,true,$10)`,
			credentialID, credentialHash[:], actor.AccountID, actor.SessionID, in.Provider, in.DeviceLabel, in.AppVersion,
			sealed, p.fingerprint("registration:"+in.Token), requestHash)
		if err != nil {
			var duplicate *pgconn.PgError
			if errors.As(err, &duplicate) && duplicate.Code == "23505" {
				return NativePushBindResult{}, ErrPushConflict
			}
			return NativePushBindResult{}, pushError(err)
		}
		status = PushInstallationStatus{ID: credentialID, Provider: in.Provider, DeviceLabel: in.DeviceLabel,
			Status: "ready", BindingGeneration: 1, RegistrationVersion: 1, LeaseExpiresAt: &expires, LastSeenAt: time.Now().UTC()}
	} else {
		sealed, err = p.seal(prior.ID, "token", []byte(in.Token))
		if err != nil {
			return NativePushBindResult{}, err
		}
		hash := credentialHash
		current, err := scanPushInstallation(tx.QueryRow(ctx, `SELECT `+pushInstallationColumns+`
			FROM notifications.push_installations WHERE id=$1 AND credential_hash=$2 AND status<>'revoked' FOR UPDATE`, prior.ID, hash[:]))
		if errors.Is(err, pgx.ErrNoRows) {
			return NativePushBindResult{}, ErrPushUnauthorized
		}
		if err != nil {
			return NativePushBindResult{}, err
		}
		var stored []byte
		if err = tx.QueryRow(ctx, `SELECT native_bind_fingerprint FROM notifications.push_installations WHERE id=$1`, current.ID).Scan(&stored); err != nil {
			return NativePushBindResult{}, err
		}
		if current.AccountID == actor.AccountID && current.SessionID == actor.SessionID && hmac.Equal(stored, requestHash) {
			status = nativeInstallationStatus(current, expires)
			if err = tx.Commit(ctx); err != nil {
				return NativePushBindResult{}, err
			}
			return NativePushBindResult{Installation: status, AccountID: actor.AccountID, SessionID: actor.SessionID}, nil
		}
		if current.Generation != *in.BindingGeneration || current.Version != *in.RegistrationVersion {
			return NativePushBindResult{}, ErrPushConflict
		}
		if current.AccountID != "" && current.AccountID != actor.AccountID && !in.AllowAccountChange {
			return NativePushBindResult{}, ErrPushAccountChange
		}
		changed := current.AccountID != actor.AccountID || current.SessionID != actor.SessionID
		if changed {
			if err = p.cancelInstallation(ctx, tx, current.ID, "binding_changed"); err != nil {
				return NativePushBindResult{}, err
			}
		}
		var generation, version int64
		err = tx.QueryRow(ctx, `UPDATE notifications.push_installations SET account_id=$2,session_id=$3,
			encrypted_token=$4,token_fingerprint=$5,provider=$6,permission='granted',status='ready',
			token_valid=true,provider_available=true,device_label=$7,app_version=$8,
			binding_generation=binding_generation+CASE WHEN $9 THEN 1 ELSE 0 END,
			registration_version=registration_version+1,last_pairing_id=NULL,native_bind_fingerprint=$10,updated_at=now()
			WHERE id=$1 RETURNING binding_generation,registration_version`, current.ID, actor.AccountID,
			actor.SessionID, sealed, p.fingerprint("registration:"+in.Token), in.Provider,
			in.DeviceLabel, in.AppVersion, changed, requestHash).Scan(&generation, &version)
		if err != nil {
			return NativePushBindResult{}, pushError(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE notifications.push_pairings SET consumed_at=now()
			WHERE installation_id=$1 AND consumed_at IS NULL`, current.ID); err != nil {
			return NativePushBindResult{}, err
		}
		status = PushInstallationStatus{ID: current.ID, Provider: in.Provider, DeviceLabel: in.DeviceLabel,
			Status: "ready", BindingGeneration: generation, RegistrationVersion: version,
			LeaseExpiresAt: &expires, LastSeenAt: time.Now().UTC()}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO notifications.push_account_settings(account_id,default_enabled)
		VALUES ($1,true) ON CONFLICT(account_id) DO NOTHING`, actor.AccountID); err != nil {
		return NativePushBindResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return NativePushBindResult{}, err
	}
	return NativePushBindResult{Installation: status, AccountID: actor.AccountID,
		SessionID: actor.SessionID}, nil
}

func (p *Push) NativeInstallation(ctx context.Context, actor authcontracts.Principal, credential string) (NativePushBindResult, error) {
	if actor.ClientKind != "android" {
		return NativePushBindResult{}, ErrPushUnauthorized
	}
	id, ok := nativeCredentialID(credential)
	if !ok {
		return NativePushBindResult{}, ErrPushUnauthorized
	}
	if err := p.rateInstallation(ctx, credential); err != nil {
		return NativePushBindResult{}, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return NativePushBindResult{}, err
	}
	defer tx.Rollback(ctx)
	expires, err := p.sessionLease(ctx, tx, actor.AccountID, actor.SessionID)
	if err != nil {
		return NativePushBindResult{}, err
	}
	hash := sha256.Sum256([]byte(credential))
	i, err := scanPushInstallation(tx.QueryRow(ctx, `SELECT `+pushInstallationColumns+`
		FROM notifications.push_installations WHERE id=$1 AND credential_hash=$2 AND status<>'revoked' FOR SHARE`, id, hash[:]))
	if errors.Is(err, pgx.ErrNoRows) {
		return NativePushBindResult{}, ErrPushUnauthorized
	}
	if err != nil {
		return NativePushBindResult{}, err
	}
	if i.AccountID != actor.AccountID || i.SessionID != actor.SessionID {
		return NativePushBindResult{}, ErrPushReconnect
	}
	status := nativeInstallationStatus(i, expires)
	return NativePushBindResult{Installation: status, AccountID: actor.AccountID, SessionID: actor.SessionID}, nil
}

func (p *Push) bindNativePush(w http.ResponseWriter, r *http.Request) {
	actor, ok := authcontracts.FromContext(r.Context())
	if !ok || actor.ClientKind != "android" {
		pushFailure(w, ErrPushUnauthorized)
		return
	}
	var in NativePushBindInput
	if !pushDecode(w, r, &in) {
		return
	}
	result, err := p.BindNative(r.Context(), actor, in)
	if err != nil {
		pushFailure(w, err)
		return
	}
	messengerWrite(w, 200, result)
}

func (p *Push) getNativePushInstallation(w http.ResponseWriter, r *http.Request) {
	actor, ok := authcontracts.FromContext(r.Context())
	if !ok || actor.ClientKind != "android" {
		pushFailure(w, ErrPushUnauthorized)
		return
	}
	result, err := p.NativeInstallation(r.Context(), actor, r.Header.Get("X-Heartbeat-Installation-Credential"))
	if err != nil {
		pushFailure(w, err)
		return
	}
	messengerWrite(w, 200, result)
}
