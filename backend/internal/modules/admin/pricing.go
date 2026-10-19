package admin

import (
	"context"
	"encoding/json"
	"errors"
	auth "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
	"net/http"
)

type PricingInput struct {
	PricePerMonitorMinor int    `json:"pricePerMonitorMinor"`
	Version              int64  `json:"version"`
	Reason               string `json:"reason"`
}

func (m *Module) UpdatePricing(ctx context.Context, p auth.Principal, in PricingInput) (contracts.Pricing, error) {
	if !validReason(in.Reason) || in.Version < 1 || in.PricePerMonitorMinor < 100 || in.PricePerMonitorMinor > 1000000 {
		return contracts.Pricing{}, ErrInvalid
	}
	mut, err := m.beginMutation(ctx, p, p.AccountID)
	if err != nil {
		return contracts.Pricing{}, err
	}
	defer mut.tx.Rollback(ctx)
	before, after, err := m.billing.UpdatePricingTx(ctx, mut.tx, in.Version, in.PricePerMonitorMinor)
	if errors.Is(err, billing.ErrPricingConflict) {
		return after, ErrConflict
	}
	if err != nil {
		return after, err
	}
	if before.Version != after.Version {
		b, err := json.Marshal(before)
		if err != nil {
			return after, err
		}
		a, err := json.Marshal(after)
		if err != nil {
			return after, err
		}
		_, err = mut.tx.Exec(ctx, `INSERT INTO admin.audit_events(actor_account_id,actor_session_id,target_account_id,target_id,action,reason,before_state,after_state,metadata) VALUES($1,$2,NULL,NULL,'billing.pricing_updated',$3,$4,$5,'{"setting":"monthly_monitor"}'::jsonb)`, p.AccountID, p.SessionID, in.Reason, b, a)
		if err != nil {
			return after, err
		}
	}
	return after, mut.tx.Commit(ctx)
}
func (m *Module) getPricing(w http.ResponseWriter, r *http.Request) {
	result, err := m.billing.Pricing(r.Context())
	if err != nil {
		write(w, 503, map[string]string{"error": "temporarily_unavailable"})
		return
	}
	write(w, 200, map[string]any{"pricing": result})
}
func (m *Module) putPricing(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in PricingInput
	if !decode(w, r, &in) {
		return
	}
	result, err := m.UpdatePricing(r.Context(), p, in)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]any{"pricing": result})
}
