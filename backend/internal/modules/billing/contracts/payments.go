package contracts

import (
	"context"
	"time"
)

type Order struct {
	ID           string     `json:"id"`
	Provider     string     `json:"provider"`
	Status       string     `json:"status"`
	MonitorCount int        `json:"monitorCount"`
	AmountMinor  int        `json:"amountMinor"`
	Currency     string     `json:"currency"`
	CreatedAt    time.Time  `json:"createdAt"`
	ExpiresAt    time.Time  `json:"expiresAt"`
	CompletedAt  *time.Time `json:"completedAt"`
	ActiveUntil  *time.Time `json:"activeUntil"`
}

type Billing struct {
	Provider             string  `json:"provider"`
	CanSimulate          bool    `json:"canSimulate"`
	PricePerMonitorMinor int     `json:"pricePerMonitorMinor"`
	Orders               []Order `json:"orders"`
}

// PaymentReport is always TEST money: it contains no customer contact data.
// OrderID is stable across retries. A sender may receive it again after losing an acknowledgement.
type PaymentReport struct {
	OrderID      string
	AccountID    string
	MonitorCount int
	AmountMinor  int
	Currency     string
	Test         bool
	CompletedAt  time.Time
}

type ReportSender interface {
	SendPaymentReport(context.Context, PaymentReport) error
}

// ReportSendError contains only bounded provider diagnostics. Provider response
// bodies, request URLs and underlying network errors must not be copied here.
type ReportSendError struct {
	Code       string
	HTTPStatus int
	RetryAfter time.Duration
}

func (e *ReportSendError) Error() string { return e.Code }
