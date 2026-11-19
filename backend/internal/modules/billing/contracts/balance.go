package contracts

import "time"

// Balance presents the wallet in kopecks while retaining whether a fractional
// balance is still available for monitoring.
type Balance struct {
	AvailableMinor       int64      `json:"availableMinor"`
	PositiveBelowCent    bool       `json:"positiveBelowCent"`
	Currency             string     `json:"currency"`
	EnabledMonitors      int        `json:"enabledMonitors"`
	AsOf                 time.Time  `json:"asOf"`
	FundedUntil          *time.Time `json:"fundedUntil"`
	Suspended            bool       `json:"suspended"`
	PricePerMonitorMinor int        `json:"pricePerMonitorMinor"`
	PriceVersion         int64      `json:"priceVersion"`
	AccountActive        bool       `json:"-"`
	ActivityEpoch        int64      `json:"-"`
	ActiveMonitors       int        `json:"-"`
}

type LedgerEntry struct {
	ID                string     `json:"id"`
	Kind              string     `json:"kind"`
	AmountMinor       int64      `json:"amountMinor"`
	PositiveBelowCent bool       `json:"positiveBelowCent,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	PeriodStart       *time.Time `json:"periodStart,omitempty"`
	PeriodEnd         *time.Time `json:"periodEnd,omitempty"`
}

type LedgerPage struct {
	Entries    []LedgerEntry `json:"entries"`
	Pagination Pagination    `json:"pagination"`
}
