package contracts

import probe "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"

// Attempt binds an execution lease to the account, monitor and immutable probe request.
// ConfigVersion and AttemptToken invalidate results after edits or lease recovery.
type Attempt struct {
	AccountID string
	MonitorID string
	Request   probe.Request
}
