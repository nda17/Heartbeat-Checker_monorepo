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
