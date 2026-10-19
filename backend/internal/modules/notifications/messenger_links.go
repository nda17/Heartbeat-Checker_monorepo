package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

func (m *Messenger) accountActive(ctx context.Context, tx pgx.Tx, accountID string) (bool, error) {
	var active bool
	err := tx.QueryRow(ctx, `SELECT a.status='active' FROM accounts.accounts a WHERE a.id=$1 FOR SHARE`, accountID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrMessengerNotFound
	}
	return active, err
}

func (m *Messenger) CreateLink(ctx context.Context, accountID, provider, kind string) (MessengerLink, string, string, string, error) {
	if !m.cfg.Enabled {
		return MessengerLink{}, "", "", "", ErrMessengerUnavailable
	}
	if !messengerKind(provider, kind) {
		return MessengerLink{}, "", "", "", ErrMessengerInvalid
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return MessengerLink{}, "", "", "", err
	}
	defer tx.Rollback(ctx)
	active, err := m.accountActive(ctx, tx, accountID)
	if err != nil {
		return MessengerLink{}, "", "", "", err
	}
	if !active {
		return MessengerLink{}, "", "", "", ErrMessengerNotFound
	}
	if provider == "telegram" && kind != "private" {
		var linked bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notifications.messenger_destinations WHERE account_id=$1 AND provider='telegram' AND kind='private' AND status='ready')`, accountID).Scan(&linked); err != nil {
			return MessengerLink{}, "", "", "", err
		}
		if !linked {
			return MessengerLink{}, "", "", "", ErrTelegramPrivateRequired
		}
	}
	id, err := outbox.ID()
	if err != nil {
		return MessengerLink{}, "", "", "", err
	}
	token, hash, err := messengerToken()
	if err != nil {
		return MessengerLink{}, "", "", "", err
	}
	link := MessengerLink{ID: id, Provider: provider, Kind: kind, Status: "pending", ExpiresAt: time.Now().Add(10 * time.Minute), Candidate: nil}
	if err = tx.QueryRow(ctx, `INSERT INTO notifications.messenger_links(id,account_id,provider,kind,token_hash,expires_at) VALUES($1,$2,$3,$4,$5,now()+interval '10 minutes') RETURNING expires_at`, id, accountID, provider, kind, hash).Scan(&link.ExpiresAt); err != nil {
		return MessengerLink{}, "", "", "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return MessengerLink{}, "", "", "", err
	}
	var startURL, command string
	if provider == "telegram" && kind == "private" {
		startURL = "https://t.me/" + m.cfg.TelegramBotUsername + "?start=" + url.QueryEscape(token)
	}
	if provider == "max" {
		startURL = m.cfg.MAXBotURL + "?start=" + url.QueryEscape(token)
	}
	if kind != "private" {
		command = "/connect@" + m.cfg.TelegramBotUsername + " " + token
	}
	return link, token, startURL, command, nil
}

func (m *Messenger) GetLink(ctx context.Context, accountID, id string) (MessengerLink, error) {
	if !m.cfg.Enabled {
		return MessengerLink{}, ErrMessengerUnavailable
	}
	if !messengerValidID(id) {
		return MessengerLink{}, ErrMessengerNotFound
	}
	var link MessengerLink
	var encrypted []byte
	var destination *string
	err := m.pool.QueryRow(ctx, `SELECT id::text,provider,kind,CASE WHEN expires_at<=now() AND status IN ('pending','observed','candidate') THEN 'expired' ELSE status END,CASE WHEN expires_at<=now() AND status IN ('pending','observed','candidate') THEN 'link_expired' ELSE error_code END,expires_at,encrypted_candidate,destination_id::text FROM notifications.messenger_links WHERE account_id=$1 AND id=$2`, accountID, id).Scan(&link.ID, &link.Provider, &link.Kind, &link.Status, &link.ErrorCode, &link.ExpiresAt, &encrypted, &destination)
	if errors.Is(err, pgx.ErrNoRows) {
		return MessengerLink{}, ErrMessengerNotFound
	}
	if err != nil {
		return MessengerLink{}, err
	}
	if link.Status == "candidate" {
		recipient, err := m.readRecipient(accountID, id, "candidate", encrypted)
		if err != nil {
			return MessengerLink{}, err
		}
		link.Candidate = recipient.publicCandidate()
	}
	link.DestinationID = destination
	return link, nil
}

func (m *Messenger) ListDestinations(ctx context.Context, accountID string) ([]MessengerDestination, error) {
	if err := m.enabled(); err != nil {
		return nil, err
	}
	var currentEmail string
	if m.cfg.EmailEnabled {
		tx, err := m.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)
		active, err := m.accountActive(ctx, tx, accountID)
		if err != nil {
			return nil, err
		}
		if !active {
			return nil, ErrMessengerNotFound
		}
		currentEmail, err = m.cfg.EmailAddressReader(ctx, tx, accountID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, authcontracts.ErrEmailUnverified) {
			return nil, err
		}
		if err != nil {
			currentEmail = ""
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
	}
	rows, err := m.pool.Query(ctx, `SELECT id::text,provider,kind,encrypted_target,status='ready',version,email_proof_kind,email_verified_at FROM notifications.messenger_destinations WHERE account_id=$1 AND (provider='email' AND $2 OR provider IN ('telegram','max') AND $3) ORDER BY created_at,id`, accountID, m.cfg.EmailEnabled, m.cfg.Enabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []MessengerDestination{}
	for rows.Next() {
		var d MessengerDestination
		var encrypted []byte
		var proofKind *string
		var verifiedAt *time.Time
		if err = rows.Scan(&d.ID, &d.Provider, &d.Kind, &encrypted, &d.Ready, &d.Version, &proofKind, &verifiedAt); err != nil {
			return nil, err
		}
		recipient, err := m.readRecipient(accountID, d.ID, "target", encrypted)
		if err != nil {
			return nil, err
		}
		d.Label, d.ChatID, d.UserID = recipient.Label, recipient.Target.ChatID, recipient.Target.UserID
		d.Email = recipient.Target.Email
		if d.Provider == "email" {
			d.Ready = d.Ready && proofKind != nil && emailProofReady(*proofKind, verifiedAt, d.Email, currentEmail)
		}
		if recipient.Target.ThreadID != "" {
			d.ThreadID = &recipient.Target.ThreadID
		}
		items = append(items, d)
	}
	return items, rows.Err()
}

func (m *Messenger) ConfirmLink(ctx context.Context, accountID, id string) (MessengerDestination, error) {
	if !m.cfg.Enabled {
		return MessengerDestination{}, ErrMessengerUnavailable
	}
	if !messengerValidID(id) {
		return MessengerDestination{}, ErrMessengerNotFound
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return MessengerDestination{}, err
	}
	defer tx.Rollback(ctx)
	active, err := m.accountActive(ctx, tx, accountID)
	if err != nil {
		return MessengerDestination{}, err
	}
	if !active {
		return MessengerDestination{}, ErrMessengerNotFound
	}
	var provider, kind, status string
	var encrypted []byte
	var expires time.Time
	err = tx.QueryRow(ctx, `SELECT provider,kind,status,encrypted_candidate,expires_at FROM notifications.messenger_links WHERE account_id=$1 AND id=$2 FOR UPDATE`, accountID, id).Scan(&provider, &kind, &status, &encrypted, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return MessengerDestination{}, ErrMessengerNotFound
	}
	if err != nil {
		return MessengerDestination{}, err
	}
	if status != "candidate" || !expires.After(time.Now()) {
		return MessengerDestination{}, ErrMessengerNotReady
	}
	recipient, err := m.readRecipient(accountID, id, "candidate", encrypted)
	if err != nil {
		return MessengerDestination{}, err
	}
	target := recipient.Target
	if target.Provider != provider || target.Kind != kind {
		return MessengerDestination{}, ErrMessengerInvalid
	}
	if kind != "private" {
		var linked bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notifications.messenger_destinations WHERE account_id=$1 AND provider='telegram' AND kind='private' AND status='ready' AND user_fingerprint=$2)`, accountID, m.fingerprint("telegram", "private", target.UserID)).Scan(&linked); err != nil {
			return MessengerDestination{}, err
		}
		if !linked {
			return MessengerDestination{}, ErrTelegramPrivateRequired
		}
	}
	fingerprint := m.recipientFingerprint(target)
	userFingerprint := m.fingerprint(provider, "private", target.UserID)
	var existingID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM notifications.messenger_destinations WHERE account_id=$1 AND provider=$2 AND recipient_fingerprint=$3 FOR NO KEY UPDATE`, accountID, provider, fingerprint).Scan(&existingID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return MessengerDestination{}, err
	}
	destinationID := existingID
	if destinationID == "" {
		destinationID, err = outbox.ID()
		if err != nil {
			return MessengerDestination{}, err
		}
	}
	plain, err := json.Marshal(recipient)
	if err != nil {
		return MessengerDestination{}, err
	}
	encryptedTarget, err := m.seal(accountID, destinationID, "target", plain)
	if err != nil {
		return MessengerDestination{}, err
	}
	if existingID == "" {
		_, err = tx.Exec(ctx, `INSERT INTO notifications.messenger_destinations(id,account_id,provider,kind,recipient_fingerprint,user_fingerprint,encrypted_target) VALUES($1,$2,$3,$4,$5,$6,$7)`, destinationID, accountID, provider, kind, fingerprint, userFingerprint, encryptedTarget)
	} else {
		_, err = tx.Exec(ctx, `UPDATE notifications.messenger_destinations SET encrypted_target=$3,user_fingerprint=$4,status='ready',version=version+1,updated_at=now() WHERE id=$1 AND account_id=$2`, destinationID, accountID, encryptedTarget, userFingerprint)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE notifications.messenger_routes SET delivery_generation=delivery_generation+1,reminder_incident_id=NULL,next_due=NULL WHERE destination_id=$1`, destinationID)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='destination_rebound' WHERE destination_id=$1 AND status IN ('pending','leased')`, destinationID)
		}
	}
	if err != nil {
		return MessengerDestination{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.messenger_links SET status='confirmed',destination_id=$3,encrypted_observation=NULL,encrypted_candidate=NULL WHERE id=$1 AND account_id=$2`, id, accountID, destinationID)
	if err != nil {
		return MessengerDestination{}, err
	}
	var d MessengerDestination
	err = tx.QueryRow(ctx, `SELECT id::text,provider,kind,status='ready',version FROM notifications.messenger_destinations WHERE id=$1`, destinationID).Scan(&d.ID, &d.Provider, &d.Kind, &d.Ready, &d.Version)
	if err != nil {
		return MessengerDestination{}, err
	}
	d.Label, d.ChatID, d.UserID = recipient.Label, target.ChatID, target.UserID
	if target.ThreadID != "" {
		d.ThreadID = &target.ThreadID
	}
	return d, tx.Commit(ctx)
}

func (m *Messenger) Disconnect(ctx context.Context, accountID, id string) error {
	if err := m.enabled(); err != nil {
		return err
	}
	if !messengerValidID(id) {
		return ErrMessengerNotFound
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	active, err := m.accountActive(ctx, tx, accountID)
	if err != nil {
		return err
	}
	if !active {
		return ErrMessengerNotFound
	}
	var status, provider string
	err = tx.QueryRow(ctx, `SELECT status,provider FROM notifications.messenger_destinations WHERE account_id=$1 AND id=$2 FOR NO KEY UPDATE`, accountID, id).Scan(&status, &provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrMessengerNotFound
	}
	if err != nil {
		return err
	}
	if provider == "email" && m.cfg.EmailCodes != nil {
		if err = m.cfg.EmailCodes.CancelNotificationEmailTx(ctx, tx, accountID); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.messenger_destinations SET status='disconnected',version=version+1,updated_at=now() WHERE id=$1`, id)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.messenger_routes SET enabled=false,settings_version=settings_version+1,delivery_generation=delivery_generation+1,reminder_incident_id=NULL,next_due=NULL WHERE destination_id=$1`, id)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='destination_disconnected' WHERE destination_id=$1 AND status IN ('pending','leased')`, id)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
