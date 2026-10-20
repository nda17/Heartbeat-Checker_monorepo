package contracts

import (
	"errors"
	"time"
)

var ErrEmailUnverified = errors.New("account email is not verified")
var ErrSessionExpired = errors.New("session expired")
var ErrSessionRevoked = errors.New("session revoked")

// Principal is the authenticated identity passed between modules.
type Principal struct {
	AccountID        string     `json:"accountId"`
	SessionID        string     `json:"sessionId"`
	Email            string     `json:"email"`
	Role             string     `json:"role"`
	MFAEnabled       bool       `json:"-"`
	MFAVerifiedUntil *time.Time `json:"-"`
	MFAStepUpUntil   *time.Time `json:"-"`
	TokenHash        []byte     `json:"-"`
}
