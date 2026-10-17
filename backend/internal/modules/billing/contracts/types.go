package contracts

import "time"

// Entitlement is the monitor capacity currently granted to an account.
// MonitorCount includes all configured monitors, including paused monitors.
type Entitlement struct {
	Quota        int        `json:"quota"`
	ActiveUntil  *time.Time `json:"activeUntil"`
	Source       string     `json:"source"`
	MonitorCount int        `json:"monitorCount"`
}
