package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

// ConnectEmail binds only the confirmed address of this active account. Merely
// connecting an address does not enable any monitor route or send a message.
func (m *Messenger) ConnectEmail(ctx context.Context, accountID string) (MessengerDestination, error) {
	if !m.cfg.EmailEnabled {
		return MessengerDestination{}, ErrMessengerUnavailable
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
	email, err := m.cfg.EmailAddressReader(ctx, tx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return MessengerDestination{}, ErrMessengerNotFound
	}
	if err != nil {
		return MessengerDestination{}, err
	}
	target := messengerTarget{Provider: "email", Kind: "email", Email: email}
	if err := target.decodeMust(); err != nil {
		return MessengerDestination{}, ErrMessengerNotFound
	}
	id, err := outbox.ID()
	if err != nil {
		return MessengerDestination{}, err
	}
	fingerprint := m.recipientFingerprint(target)
	seal := func(id string) ([]byte, error) {
		plain, err := json.Marshal(messengerRecipient{Target: target, Label: email})
		if err != nil {
			return nil, err
		}
		return m.seal(accountID, id, "target", plain)
	}
	encrypted, err := seal(id)
	if err != nil {
		return MessengerDestination{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO notifications.messenger_destinations(id,account_id,provider,kind,recipient_fingerprint,user_fingerprint,encrypted_target)
		VALUES($1,$2,'email','email',$3,$3,$4) ON CONFLICT(account_id) WHERE provider='email' DO NOTHING`, id, accountID, fingerprint, encrypted)
	if err != nil {
		return MessengerDestination{}, err
	}
	var status string
	var version int64
	err = tx.QueryRow(ctx, `SELECT id::text,status,version,encrypted_target FROM notifications.messenger_destinations
		WHERE account_id=$1 AND provider='email' FOR NO KEY UPDATE`, accountID).Scan(&id, &status, &version, &encrypted)
	if err != nil {
		return MessengerDestination{}, err
	}
	recipient, err := m.readRecipient(accountID, id, "target", encrypted)
	if err != nil {
		return MessengerDestination{}, err
	}
	if status != "ready" || recipient.Target.Email != email {
		encrypted, err = seal(id)
		if err != nil {
			return MessengerDestination{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE notifications.messenger_destinations SET encrypted_target=$2,recipient_fingerprint=$3,user_fingerprint=$3,status='ready',version=version+1,updated_at=now() WHERE id=$1`, id, encrypted, fingerprint)
		if err != nil {
			return MessengerDestination{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE notifications.messenger_routes SET enabled=false,settings_version=settings_version+1,delivery_generation=delivery_generation+1,reminder_incident_id=NULL,next_due=NULL WHERE destination_id=$1`, id)
		if err != nil {
			return MessengerDestination{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE notifications.messenger_deliveries SET status='cancelled',attempt_token=NULL,lease_until=NULL,completed_at=now(),error_code='destination_changed' WHERE destination_id=$1 AND status IN ('pending','leased')`, id)
		if err != nil {
			return MessengerDestination{}, err
		}
		version++
	}
	if err = tx.Commit(ctx); err != nil {
		return MessengerDestination{}, err
	}
	return MessengerDestination{ID: id, Provider: "email", Kind: "email", Label: email, Email: email, Ready: true, Version: version}, nil
}

func (m *Messenger) connectEmail(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	var in struct{}
	if !messengerDecode(w, r, &in) {
		return
	}
	destination, err := m.ConnectEmail(r.Context(), owner)
	if err != nil {
		messengerFailure(w, err)
		return
	}
	messengerWrite(w, 200, map[string]any{"destination": destination})
}

func emailMessage(s messengerSnapshot) (string, authcontracts.MailContent) {
	text, buttons := messengerMessage(s)
	subject := "Heartbeat Checker · сбой подтверждён"
	if s.Kind == "up" {
		subject = "Heartbeat Checker · сервис восстановился"
	}
	if s.Kind == "reminder" {
		subject = "Heartbeat Checker · сбой продолжается"
	}
	markup := `<html lang="ru"><body style="font-family:Arial,sans-serif;line-height:1.6;color:#132334"><h1 style="font-size:22px">` + html.EscapeString(subject) + `</h1><p>` + strings.ReplaceAll(html.EscapeString(text), "\n", "<br>") + `</p>`
	for _, button := range buttons {
		text += "\n\n" + button.Text + ": " + button.URL
		markup += `<p><a href="` + html.EscapeString(button.URL) + `">` + html.EscapeString(button.Text) + `</a></p>`
	}
	footer := "Вы получаете письмо, потому что включили email-уведомления для этого монитора. Отключить их можно в настройках уведомлений монитора в личном кабинете."
	text += "\n\n" + footer
	markup += `<p style="color:#607080">` + footer + `</p></body></html>`
	return subject, authcontracts.MailContent{Text: text, HTML: markup}
}

func (m *Messenger) sendEmail(ctx context.Context, a MessengerAttempt) error {
	subject, body := emailMessage(a.Snapshot)
	return m.cfg.EmailSender.SendWithID(ctx, a.Target.Email, subject, body, fmt.Sprintf("<%s@heartbeat-checker.ru>", a.ID))
}
