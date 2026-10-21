package contracts

import (
	"context"
	"time"
)

type Order struct {
	ID                   string     `json:"id"`
	PurchaseKind         string     `json:"purchaseKind"`
	PackageID            *string    `json:"packageId,omitempty"`
	FulfilledPackageID   *string    `json:"fulfilledPackageId,omitempty"`
	Provider             string     `json:"provider"`
	Status               string     `json:"status"`
	FulfillmentStatus    string     `json:"fulfillmentStatus"`
	ReconciliationStatus string     `json:"reconciliationStatus"`
	FulfillmentReason    string     `json:"fulfillmentReason,omitempty"`
	MonitorCount         int        `json:"monitorCount"`
	AmountMinor          int        `json:"amountMinor"`
	Currency             string     `json:"currency"`
	ConfirmationURL      string     `json:"confirmationUrl,omitempty"`
	Test                 bool       `json:"test"`
	LastErrorCode        string     `json:"lastErrorCode,omitempty"`
	CreatedAt            time.Time  `json:"createdAt"`
	ExpiresAt            *time.Time `json:"expiresAt"`
	CompletedAt          *time.Time `json:"completedAt"`
	ActiveUntil          *time.Time `json:"activeUntil"`
}

type Billing struct {
	Provider             string  `json:"provider"`
	CanSimulate          bool    `json:"canSimulate"`
	CanPay               bool    `json:"canPay"`
	Test                 bool    `json:"test"`
	AccessUnchanged      bool    `json:"accessUnchanged"`
	PricePerMonitorMinor int     `json:"pricePerMonitorMinor"`
	PriceVersion         int64   `json:"priceVersion"`
	Orders               []Order `json:"orders"`
}

type ProviderPayment struct {
	ID              string
	Status          string
	AmountValue     string
	Currency        string
	OrderID         string
	ShopID          string
	ConfirmationURL string
	Test            *bool
	Paid            bool
}

type Provider interface {
	CreatePayment(context.Context, string, []byte) (ProviderPayment, error)
	GetPayment(context.Context, string) (ProviderPayment, error)
}

// ProviderNotification deliberately keeps only routing and reconciliation data.
// Extra provider fields, including payment-card details, are ignored by JSON decoding.
type ProviderNotification struct {
	Type   string `json:"type"`
	Event  string `json:"event"`
	Object struct {
		ID       string `json:"id"`
		Metadata struct {
			OrderID string `json:"order_id"`
		} `json:"metadata"`
	} `json:"object"`
}

// PaymentReport contains no customer contact data. Test is stored on the order.
// OrderID is stable across retries. A sender may receive it again after losing an acknowledgement.
type PaymentReport struct {
	OrderID           string
	AccountID         string
	Provider          string
	FulfillmentStatus string
	MonitorCount      int
	AmountMinor       int
	Currency          string
	Test              bool
	CompletedAt       time.Time
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
