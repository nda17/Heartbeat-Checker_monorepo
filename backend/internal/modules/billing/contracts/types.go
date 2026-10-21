package contracts

import "time"

// Entitlement is the monitor capacity currently granted to an account.
// MonitorCount includes all configured monitors, including paused monitors.
type Entitlement struct {
	Quota               int            `json:"quota"`
	ActiveUntil         *time.Time     `json:"activeUntil"`
	Source              string         `json:"source"`
	MonitorCount        int            `json:"monitorCount"`
	EnabledMonitorCount int            `json:"enabledMonitorCount"`
	ActiveMonitorCount  int            `json:"activeMonitorCount"`
	Version             int64          `json:"version"`
	RenewalAnchorDay    int            `json:"-"`
	Admin               *AdminGrant    `json:"admin"`
	Paid                *Subscription  `json:"paid"`
	Packages            []Subscription `json:"packages"`
}

// AdminGrant is the independently versioned administrative entitlement.
// Expired grants remain present so consumers can explain their history.
type AdminGrant struct {
	Quota       int        `json:"quota"`
	ActiveUntil *time.Time `json:"activeUntil"`
	Version     int64      `json:"version"`
}

// Subscription is the paid entitlement independently granted by billing.
// Expired subscriptions remain present for history and support views.
type Subscription struct {
	ID               string     `json:"id"`
	Quota            int        `json:"quota"`
	ActiveUntil      *time.Time `json:"activeUntil"`
	Active           bool       `json:"active"`
	Source           string     `json:"source"`
	Version          int64      `json:"version"`
	RenewalAnchorDay int        `json:"-"`
}
