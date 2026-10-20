package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

// ConnectEmail preserves the legacy create/idempotent-only contract.
func (m *Messenger) ConnectEmail(ctx context.Context, accountID string) (MessengerDestination, error) {
	d, _, err := m.ConnectEmailAddress(ctx, accountID, "", nil, 0, "", true)
	return d, err
}

type emailDestinationState struct {
	id, status, email string
	version           int64
}

func (m *Messenger) emailTransaction(ctx context.Context, accountID string, requireVerified bool) (pgx.Tx, string, error) {
	if !m.cfg.EmailEnabled {
		return nil, "", ErrMessengerUnavailable
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT status='active' FROM accounts.accounts WHERE id=$1 FOR NO KEY UPDATE`, accountID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		err = ErrMessengerNotFound
	}
	var email string
	if err == nil {
		email, err = m.cfg.EmailAddressReader(ctx, tx, accountID)
		if requireVerified && errors.Is(err, pgx.ErrNoRows) {
			err = ErrMessengerNotFound
		}
		if !requireVerified && (errors.Is(err, pgx.ErrNoRows) || errors.Is(err, authcontracts.ErrEmailUnverified)) {
			email, err = "", nil
		}
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, "", err
	}
	return tx, email, nil
}

func (m *Messenger) emailDestinationTx(ctx context.Context, tx pgx.Tx, accountID string) (emailDestinationState, error) {
	var d emailDestinationState
	var encrypted []byte
	err := tx.QueryRow(ctx, `SELECT id::text,status,version,encrypted_target FROM notifications.messenger_destinations WHERE account_id=$1 AND provider='email' FOR NO KEY UPDATE`, accountID).Scan(&d.id, &d.status, &d.version, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	recipient, err := m.readRecipient(accountID, d.id, "target", encrypted)
	d.email = recipient.Target.Email
	return d, err
}

func emailSnapshotMatches(d emailDestinationState, id *string, version int64) bool {
	return id == nil && d.id == "" && version == 0 || id != nil && *id == d.id && version == d.version
}

// ConnectEmailAddress leaves the previous destination and routes intact while a
// different address is being proved. Only an explicit matching snapshot replaces it.
func (m *Messenger) ConnectEmailAddress(ctx context.Context, accountID, email string, destinationID *string, version int64, ip string, legacy bool) (MessengerDestination, *authcontracts.EmailCodeChallenge, error) {
	if version < 0 || destinationID != nil && (!messengerValidID(*destinationID) || version < 1) || destinationID == nil && version != 0 {
		return MessengerDestination{}, nil, ErrMessengerInvalid
	}
	email = strings.ToLower(strings.TrimSpace(email))
	budgeted := false
	if !legacy {
		requestedTarget := messengerTarget{Provider: "email", Kind: "email", Email: email}
		if requestedTarget.decodeMust() != nil {
			return MessengerDestination{}, nil, ErrMessengerInvalid
		}
		// Inspect eligibility in a short transaction, close it, then spend the
		// durable send budget before taking mutation locks. The actual write
		// repeats the credential check and fails closed if it now needs a code.
		preflightTx, confirmedEmail, err := m.emailTransaction(ctx, accountID, false)
		if err != nil {
			return MessengerDestination{}, nil, err
		}
		if err = preflightTx.Rollback(ctx); err != nil {
			return MessengerDestination{}, nil, err
		}
		if email != confirmedEmail || confirmedEmail == "" {
			if m.cfg.EmailCodes == nil {
				return MessengerDestination{}, nil, ErrMessengerUnavailable
			}
			if err = m.cfg.EmailCodes.PreflightNotificationEmailStart(ctx, authcontracts.NotificationEmailInput{AccountID: accountID, Email: email, DestinationID: destinationID, Version: version}, ip); err != nil {
				return MessengerDestination{}, nil, err
			}
			budgeted = true
		}
	}
	tx, currentEmail, err := m.emailTransaction(ctx, accountID, legacy)
	if err != nil {
		return MessengerDestination{}, nil, err
	}
	defer tx.Rollback(ctx)
	d, err := m.emailDestinationTx(ctx, tx, accountID)
	if err != nil {
		return MessengerDestination{}, nil, err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if legacy {
		if currentEmail == "" {
			return MessengerDestination{}, nil, authcontracts.ErrEmailUnverified
		}
		email = currentEmail
		if d.id != "" && d.email != email {
			return MessengerDestination{}, nil, ErrMessengerConflict
		}
	} else if !emailSnapshotMatches(d, destinationID, version) {
		return MessengerDestination{}, nil, ErrMessengerConflict
	}
	target := messengerTarget{Provider: "email", Kind: "email", Email: email}
	if target.decodeMust() != nil {
		return MessengerDestination{}, nil, ErrMessengerInvalid
	}
	if email == currentEmail && currentEmail != "" {
		result, err := m.applyEmailTx(ctx, tx, accountID, email, "account_email", d)
		if err != nil {
			return MessengerDestination{}, nil, err
		}
		if m.cfg.EmailCodes != nil {
			if err = m.cfg.EmailCodes.CancelNotificationEmailTx(ctx, tx, accountID); err != nil {
				return MessengerDestination{}, nil, err
			}
		}
		return result, nil, tx.Commit(ctx)
	}
	if m.cfg.EmailCodes == nil {
		return MessengerDestination{}, nil, ErrMessengerUnavailable
	}
	if !budgeted {
		return MessengerDestination{}, nil, ErrMessengerConflict
	}
	challenge, err := m.cfg.EmailCodes.StartNotificationEmailTx(ctx, tx, authcontracts.NotificationEmailInput{AccountID: accountID, Email: email, DestinationID: destinationID, Version: version}, ip)
	if err != nil {
		return MessengerDestination{}, nil, err
	}
	return MessengerDestination{}, &challenge, tx.Commit(ctx)
}

func (m *Messenger) applyEmailTx(ctx context.Context, tx pgx.Tx, accountID, email, proofKind string, d emailDestinationState) (MessengerDestination, error) {
	if d.id != "" && d.status == "ready" && d.email == email {
		// Re-proving the same address preserves route opt-in and destination
		// version, but makes a legacy account proof independent when needed.
		if proofKind == "code" {
			if _, err := tx.Exec(ctx, `UPDATE notifications.messenger_destinations SET email_proof_kind='code',email_verified_at=now() WHERE id=$1`, d.id); err != nil {
				return MessengerDestination{}, err
			}
		}
		return MessengerDestination{ID: d.id, Provider: "email", Kind: "email", Label: email, Email: email, Ready: true, Version: d.version}, nil
	}
	id := d.id
	var err error
	if id == "" {
		id, err = outbox.ID()
		if err != nil {
			return MessengerDestination{}, err
		}
	}
	target := messengerTarget{Provider: "email", Kind: "email", Email: email}
	plain, err := json.Marshal(messengerRecipient{Target: target, Label: email})
	if err != nil {
		return MessengerDestination{}, err
	}
	encrypted, err := m.seal(accountID, id, "target", plain)
	if err != nil {
		return MessengerDestination{}, err
	}
	fingerprint := m.recipientFingerprint(target)
	version := int64(1)
	if d.id == "" {
		_, err = tx.Exec(ctx, `INSERT INTO notifications.messenger_destinations(id,account_id,provider,kind,recipient_fingerprint,user_fingerprint,encrypted_target,email_proof_kind,email_verified_at) VALUES($1,$2,'email','email',$3,$3,$4,$5,now())`, id, accountID, fingerprint, encrypted, proofKind)
	} else {
		_, err = tx.Exec(ctx, `UPDATE notifications.messenger_destinations SET encrypted_target=$2,recipient_fingerprint=$3,user_fingerprint=$3,status='ready',email_proof_kind=$4,email_verified_at=now(),version=version+1,updated_at=now() WHERE id=$1`, id, encrypted, fingerprint, proofKind)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE notifications.messenger_routes SET enabled=false,settings_version=settings_version+1,delivery_generation=delivery_generation+1,reminder_incident_id=NULL,next_due=NULL WHERE destination_id=$1`, id)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='destination_changed' WHERE destination_id=$1 AND status IN ('pending','leased')`, id)
		}
		version = d.version + 1
	}
	if err != nil {
		return MessengerDestination{}, err
	}
	return MessengerDestination{ID: id, Provider: "email", Kind: "email", Label: email, Email: email, Ready: true, Version: version}, nil
}

func (m *Messenger) ConfirmEmail(ctx context.Context, accountID, id, code, ip string) (MessengerDestination, error) {
	if !messengerValidID(id) {
		return MessengerDestination{}, ErrMessengerNotFound
	}
	if m.cfg.EmailCodes == nil {
		return MessengerDestination{}, ErrMessengerUnavailable
	}
	if !m.cfg.EmailEnabled {
		return MessengerDestination{}, ErrMessengerUnavailable
	}
	if err := m.cfg.EmailCodes.PreflightNotificationEmailConfirm(ctx, accountID, id, code, ip); err != nil {
		return MessengerDestination{}, err
	}
	tx, _, err := m.emailTransaction(ctx, accountID, false)
	if err != nil {
		return MessengerDestination{}, err
	}
	defer tx.Rollback(ctx)
	d, err := m.emailDestinationTx(ctx, tx, accountID)
	if err != nil {
		return MessengerDestination{}, err
	}
	proof, valid, err := m.cfg.EmailCodes.ConfirmNotificationEmailTx(ctx, tx, accountID, id, code, ip)
	if err != nil {
		return MessengerDestination{}, err
	}
	if !valid {
		if err = tx.Commit(ctx); err != nil {
			return MessengerDestination{}, err
		}
		return MessengerDestination{}, authcontracts.ErrEmailCodeInvalid
	}
	if !emailSnapshotMatches(d, proof.DestinationID, proof.Version) {
		return MessengerDestination{}, ErrMessengerConflict
	}
	result, err := m.applyEmailTx(ctx, tx, accountID, proof.Email, "code", d)
	if err != nil {
		return MessengerDestination{}, err
	}
	if err = m.cfg.EmailCodes.CancelNotificationEmailTx(ctx, tx, accountID); err != nil {
		return MessengerDestination{}, err
	}
	return result, tx.Commit(ctx)
}

func (m *Messenger) connectEmail(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	var in struct {
		Email         *string `json:"email"`
		DestinationID *string `json:"destinationId"`
		Version       int64   `json:"version"`
	}
	if !messengerDecode(w, r, &in) {
		return
	}
	email := ""
	if in.Email != nil {
		email = *in.Email
	}
	if in.Email == nil && (in.DestinationID != nil || in.Version != 0) {
		messengerFailure(w, ErrMessengerInvalid)
		return
	}
	d, challenge, err := m.ConnectEmailAddress(r.Context(), owner, email, in.DestinationID, in.Version, messengerIP(r), in.Email == nil)
	if err != nil {
		messengerFailure(w, err)
		return
	}
	if challenge != nil {
		messengerWrite(w, 202, map[string]any{"challenge": challenge})
		return
	}
	messengerWrite(w, 200, map[string]any{"destination": d})
}

func messengerIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func (m *Messenger) emailVerification(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	if !m.cfg.EmailEnabled || m.cfg.EmailCodes == nil {
		messengerFailure(w, ErrMessengerUnavailable)
		return
	}
	challenge, err := m.cfg.EmailCodes.CurrentNotificationEmail(r.Context(), owner)
	if errors.Is(err, authcontracts.ErrEmailCodeInvalid) {
		messengerWrite(w, 200, map[string]any{"challenge": nil})
		return
	}
	if err != nil {
		messengerFailure(w, err)
		return
	}
	messengerWrite(w, 200, map[string]any{"challenge": challenge})
}

func (m *Messenger) resendEmailVerification(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	var in struct{}
	if !messengerDecode(w, r, &in) {
		return
	}
	if !messengerValidID(r.PathValue("id")) {
		messengerFailure(w, ErrMessengerNotFound)
		return
	}
	if m.cfg.EmailCodes == nil {
		messengerFailure(w, ErrMessengerUnavailable)
		return
	}
	if !m.cfg.EmailEnabled {
		messengerFailure(w, ErrMessengerUnavailable)
		return
	}
	if err := m.cfg.EmailCodes.PreflightNotificationEmailResend(r.Context(), owner, r.PathValue("id"), messengerIP(r)); err != nil {
		messengerFailure(w, err)
		return
	}
	tx, _, err := m.emailTransaction(r.Context(), owner, false)
	if err != nil {
		messengerFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	challenge, err := m.cfg.EmailCodes.ResendNotificationEmailTx(r.Context(), tx, owner, r.PathValue("id"), messengerIP(r))
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		messengerFailure(w, err)
		return
	}
	messengerWrite(w, 202, map[string]any{"challenge": challenge})
}

func (m *Messenger) confirmEmailVerification(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if !messengerDecode(w, r, &in) {
		return
	}
	d, err := m.ConfirmEmail(r.Context(), owner, r.PathValue("id"), in.Code, messengerIP(r))
	if err != nil {
		messengerFailure(w, err)
		return
	}
	messengerWrite(w, 200, map[string]any{"destination": d})
}

// Code proofs are independent of login credentials. Legacy account proofs keep
// the existing strict current-and-verified account email guard.
func emailProofReady(kind string, verifiedAt *time.Time, targetEmail, currentEmail string) bool {
	return kind == "code" && verifiedAt != nil || kind == "account_email" && currentEmail != "" && targetEmail == currentEmail
}

func emailMessage(s messengerSnapshot) (string, authcontracts.MailContent, error) {
	text, buttons := messengerMessage(s)
	view := incidentEmailData(s, buttons)
	subject := "Heartbeat Checker · " + view.Title + " · " + incidentEmailSubjectName(view.Name)
	for _, button := range buttons {
		text += "\n\n" + button.Text + ": " + button.URL
	}
	text += "\n\n" + incidentEmailFooter
	markup, err := renderIncidentEmail(view)
	if err != nil {
		return "", authcontracts.MailContent{}, err
	}
	return subject, authcontracts.MailContent{Text: text, HTML: markup}, nil
}

func (m *Messenger) sendEmail(ctx context.Context, a MessengerAttempt) error {
	subject, body, err := emailMessage(a.Snapshot)
	if err != nil {
		return err
	}
	return m.cfg.EmailSender.SendWithID(ctx, a.Target.Email, subject, body, fmt.Sprintf("<%s@heartbeat-checker.ru>", a.ID))
}
