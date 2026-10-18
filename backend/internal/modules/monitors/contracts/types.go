package contracts

import "time"

// MonitorInput contains fields accepted when creating a monitor.
type MonitorInput struct {
	Name             string `json:"name"`
	URL              string `json:"url"`
	Method           string `json:"method"`
	ExpectedStatus   int    `json:"expectedStatus"`
	BodyContains     string `json:"bodyContains"`
	IntervalMinutes  *int   `json:"intervalMinutes,omitempty"`
	FailureThreshold *int   `json:"failureThreshold,omitempty"`
	Enabled          bool   `json:"enabled"`
}

// Patch contains only fields provided for an optimistic update.
type Patch struct {
	Name             *string `json:"name,omitempty"`
	URL              *string `json:"url,omitempty"`
	Method           *string `json:"method,omitempty"`
	ExpectedStatus   *int    `json:"expectedStatus,omitempty"`
	BodyContains     *string `json:"bodyContains,omitempty"`
	IntervalMinutes  *int    `json:"intervalMinutes,omitempty"`
	FailureThreshold *int    `json:"failureThreshold,omitempty"`
	Enabled          *bool   `json:"enabled,omitempty"`
	Version          int64   `json:"version"`
}

// Monitor is the public state of one configured monitor.
type Monitor struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	URL              string     `json:"url"`
	Method           string     `json:"method"`
	ExpectedStatus   int        `json:"expectedStatus"`
	BodyContains     string     `json:"bodyContains"`
	IntervalMinutes  int        `json:"intervalMinutes"`
	FailureThreshold int        `json:"failureThreshold"`
	Enabled          bool       `json:"enabled"`
	Version          int64      `json:"version"`
	State            string     `json:"state"`
	LastCheckedAt    *time.Time `json:"lastCheckedAt"`
	LastHTTPStatus   *int       `json:"lastHttpStatus"`
	LastLatencyMS    *int64     `json:"lastLatencyMs"`
	NextCheckAt      *time.Time `json:"nextCheckAt"`
}

// Result summarizes one completed monitor run.
type Result struct {
	ID         string    `json:"id"`
	Outcome    string    `json:"outcome"`
	ErrorCode  string    `json:"errorCode,omitempty"`
	CheckedAt  time.Time `json:"checkedAt"`
	HTTPStatus *int      `json:"httpStatus,omitempty"`
	LatencyMS  int64     `json:"latencyMs"`
}

// Incident represents a confirmed period of monitor failure.
type Incident struct {
	ID          string     `json:"id"`
	OpenedAt    time.Time  `json:"openedAt"`
	ConfirmedAt time.Time  `json:"confirmedAt"`
	ClosedAt    *time.Time `json:"closedAt,omitempty"`
	CloseReason string     `json:"closeReason,omitempty"`
}
