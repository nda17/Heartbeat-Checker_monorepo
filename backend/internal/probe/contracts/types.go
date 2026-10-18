package contracts

import "time"

// Request is one leased monitor attempt sent to an isolated probe process.
type Request struct {
	RunID          string `json:"runId"`
	AttemptToken   string `json:"attemptToken"`
	ConfigVersion  int64  `json:"configVersion"`
	URL            string `json:"url"`
	Method         string `json:"method"`
	ExpectedStatus int    `json:"expectedStatus"`
	BodyContains   string `json:"bodyContains"`
}

// Result reports the outcome of a probe attempt without exposing raw errors.
type Result struct {
	RunID         string    `json:"runId"`
	AttemptToken  string    `json:"attemptToken"`
	ConfigVersion int64     `json:"configVersion"`
	Outcome       string    `json:"outcome"`
	CheckedAt     time.Time `json:"checkedAt"`
	LatencyMS     int64     `json:"latencyMs"`
	HTTPStatus    int       `json:"httpStatus"`
	ErrorCode     string    `json:"errorCode"`
}

// WebhookRequest carries a signed payload, but never the endpoint signing key.
// Body is base64 encoded on the private worker-to-probe wire to preserve its
// exact bytes for HMAC verification by the recipient.
type WebhookRequest struct {
	URL       string `json:"url"`
	Body      []byte `json:"body"`
	EventID   string `json:"eventId"`
	Timestamp string `json:"timestamp"`
	Signature string `json:"signature"`
}

// WebhookResult contains only bounded delivery metadata, never the response body.
type WebhookResult struct {
	EventID           string `json:"eventId"`
	HTTPStatus        int    `json:"httpStatus"`
	ErrorCode         string `json:"errorCode"`
	RetryAfterSeconds int    `json:"retryAfterSeconds"`
}
