package contracts

import "time"

// ListOptions contains the validated server-side monitor list controls.
type ListOptions struct {
	Page     int
	PageSize int
	Sort     string
	Query    string
}

// Pagination describes the selected page of a filtered monitor list.
type Pagination struct {
	Page       int `json:"page"`
	PageSize   int `json:"pageSize"`
	Total      int `json:"total"`
	TotalPages int `json:"totalPages"`
}

// MonitorSummary contains account-wide status counts for the monitor list.
type MonitorSummary struct {
	Total                int `json:"total"`
	Up                   int `json:"up"`
	Down                 int `json:"down"`
	Unknown              int `json:"unknown"`
	Paused               int `json:"paused"`
	SubscriptionRequired int `json:"subscriptionRequired"`
}

// MonitorPage is the paged list response returned when list controls are used.
type MonitorPage struct {
	Monitors   []ListedMonitor `json:"monitors"`
	Pagination Pagination      `json:"pagination"`
	Summary    MonitorSummary  `json:"summary"`
}

// ListedMonitor adds active notification channels without changing the legacy
// Monitor response shape.
type ListedMonitor struct {
	Monitor
	NotificationChannels []string `json:"notificationChannels"`
}

// MonitorInput contains fields accepted when creating a monitor.
type MonitorInput struct {
	CheckType        string `json:"checkType"`
	Name             string `json:"name"`
	URL              string `json:"url"`
	TimeZone         string `json:"timeZone"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Method           string `json:"method"`
	ExpectedStatus   int    `json:"expectedStatus"`
	BodyContains     string `json:"bodyContains"`
	IntervalMinutes  *int   `json:"intervalMinutes,omitempty"`
	FailureThreshold *int   `json:"failureThreshold,omitempty"`
	Enabled          bool   `json:"enabled"`
}

// Patch contains only fields provided for an optimistic update.
type Patch struct {
	CheckType        *string `json:"checkType,omitempty"`
	Name             *string `json:"name,omitempty"`
	URL              *string `json:"url,omitempty"`
	TimeZone         *string `json:"timeZone,omitempty"`
	Host             *string `json:"host,omitempty"`
	Port             *int    `json:"port,omitempty"`
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
	CheckType        string     `json:"checkType"`
	Name             string     `json:"name"`
	URL              string     `json:"url"`
	TimeZone         string     `json:"timeZone"`
	Provider         Provider   `json:"provider"`
	Host             string     `json:"host"`
	Port             int        `json:"port"`
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
	LastRoundTripMS  *int64     `json:"lastRoundTripMs"`
	NextCheckAt      *time.Time `json:"nextCheckAt"`
}

// Provider describes the health of configured outbound providers.
type Provider struct {
	Names     []string   `json:"names"`
	Status    string     `json:"status"`
	CheckedAt *time.Time `json:"checkedAt"`
}

// Result summarizes one completed monitor run.
type Result struct {
	ID          string    `json:"id"`
	CheckType   string    `json:"checkType"`
	Outcome     string    `json:"outcome"`
	ErrorCode   string    `json:"errorCode,omitempty"`
	CheckedAt   time.Time `json:"checkedAt"`
	HTTPStatus  *int      `json:"httpStatus,omitempty"`
	LatencyMS   int64     `json:"latencyMs"`
	RoundTripMS *int64    `json:"roundTripMs"`
}

// Incident represents a confirmed period of monitor failure.
type Incident struct {
	ID          string     `json:"id"`
	Number      int64      `json:"number"`
	OpenedAt    time.Time  `json:"openedAt"`
	ConfirmedAt time.Time  `json:"confirmedAt"`
	ClosedAt    *time.Time `json:"closedAt,omitempty"`
	CloseReason string     `json:"closeReason,omitempty"`
}
