package contracts

import "time"

type Webhook struct {
	ID        string    `json:"id"`
	MonitorID string    `json:"monitorId"`
	URL       string    `json:"url"`
	Enabled   bool      `json:"enabled"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type PutWebhook struct {
	ID           string `json:"id,omitempty"`
	Version      int64  `json:"version"`
	URL          string `json:"url"`
	Enabled      bool   `json:"enabled"`
	RotateSecret bool   `json:"rotateSecret,omitempty"`
}

type DeleteWebhook struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

type Delivery struct {
	ID             string     `json:"id"`
	EventID        string     `json:"eventId"`
	Type           string     `json:"type"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	OccurredAt     time.Time  `json:"occurredAt"`
	CreatedAt      time.Time  `json:"createdAt"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
	LastHTTPStatus *int       `json:"lastHttpStatus,omitempty"`
	LastErrorCode  string     `json:"lastErrorCode,omitempty"`
}

// Attempt contains private material only inside the worker process.
type Attempt struct {
	ID              string
	AccountID       string
	MonitorID       string
	EndpointID      string
	EndpointVersion int64
	EventID         string
	Token           string
	URL             string
	Secret          string
	Body            []byte
}

type Result struct {
	HTTPStatus        int
	ErrorCode         string
	RetryAfterSeconds int
}
