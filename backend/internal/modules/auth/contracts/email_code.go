package contracts

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrEmailCodeInvalid = errors.New("invalid or expired email code")
var ErrEmailCodeRateLimited = errors.New("email code rate limited")
var ErrEmailCodeUnavailable = errors.New("email code unavailable")

type EmailCodeChallenge struct {
	ID                string    `json:"id"`
	Email             string    `json:"email"`
	ExpiresAt         time.Time `json:"expiresAt"`
	ResendAvailableAt time.Time `json:"resendAvailableAt"`
	ServerNow         time.Time `json:"serverNow"`
	Status            string    `json:"status,omitempty"`
}

type NotificationEmailInput struct {
	AccountID     string
	Email         string
	DestinationID *string
	Version       int64
}

type NotificationEmailProof struct {
	Email         string
	DestinationID *string
	Version       int64
}

// Callers first lock the active account, then the destination snapshot. All
// changes to a destination and the matching proof must use the same transaction.
// Confirm returns valid=false for a wrong code after recording an attempt; the
// caller MUST commit that transaction, then report ErrEmailCodeInvalid.
type NotificationEmailCodes interface {
	// Call before opening a transaction. Budgets persist if the mutation fails.
	PreflightNotificationEmailStart(context.Context, NotificationEmailInput, string) error
	PreflightNotificationEmailResend(context.Context, string, string, string) error
	PreflightNotificationEmailConfirm(context.Context, string, string, string, string) error
	StartNotificationEmailTx(context.Context, pgx.Tx, NotificationEmailInput, string) (EmailCodeChallenge, error)
	CancelNotificationEmailTx(context.Context, pgx.Tx, string) error
	CurrentNotificationEmail(context.Context, string) (EmailCodeChallenge, error)
	ResendNotificationEmailTx(context.Context, pgx.Tx, string, string, string) (EmailCodeChallenge, error)
	ConfirmNotificationEmailTx(context.Context, pgx.Tx, string, string, string, string) (NotificationEmailProof, bool, error)
}
