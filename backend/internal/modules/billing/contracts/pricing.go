package contracts

import "time"

type Pricing struct {
	PricePerMonitorMinor int       `json:"pricePerMonitorMinor"`
	Currency             string    `json:"currency"`
	Period               string    `json:"period"`
	Version              int64     `json:"version"`
	UpdatedAt            time.Time `json:"updatedAt"`
}
