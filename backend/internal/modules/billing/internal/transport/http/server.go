package billinghttp

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	auth "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/internal/service"
)

type Server struct{ service *service.Service }

func New(s *service.Service) *Server { return &Server{s} }
func (s *Server) Register(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	for route, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/billing/orders/{id}":           s.get,
		"GET /api/v1/billing":                       s.overview,
		"POST /api/v1/billing/orders":               s.create,
		"POST /api/v1/billing/orders/{id}/simulate": s.simulate,
	} {
		mux.Handle(route, protect(handler))
	}
	mux.HandleFunc("GET /api/v1/billing/pricing", s.pricing)
	mux.HandleFunc("POST /webhooks/billing/yookassa", s.webhook)
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, code string) {
	write(w, status, map[string]string{"error": code})
}
func failure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrOrderAmount):
		fail(w, 400, "order_amount_exceeded")
	case errors.Is(err, service.ErrInvalid):
		fail(w, 400, "invalid_request")
	case errors.Is(err, service.ErrForbidden):
		fail(w, 403, "mock_billing_forbidden")
	case errors.Is(err, service.ErrNotFound):
		fail(w, 404, "not_found")
	case errors.Is(err, service.ErrPriceChanged):
		fail(w, 409, "price_changed")
	case errors.Is(err, service.ErrConflict):
		fail(w, 409, "billing_conflict")
	default:
		fail(w, 503, "temporarily_unavailable")
	}
}
func owner(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		fail(w, 401, "unauthorized")
	}
	return p.AccountID, ok
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, 415, "json_required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		fail(w, 400, "invalid_request")
		return false
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		fail(w, 400, "invalid_request")
		return false
	}
	return true
}
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	account, ok := owner(w, r)
	if !ok {
		return
	}
	result, err := s.service.Overview(r.Context(), account)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, result)
}
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	account, ok := owner(w, r)
	if !ok {
		return
	}
	var input struct {
		MonitorCount   int    `json:"monitorCount"`
		IdempotencyKey string `json:"idempotencyKey"`
		PriceVersion   int64  `json:"priceVersion"`
	}
	if !decode(w, r, &input) {
		return
	}
	order, err := s.service.CreateOrder(r.Context(), account, input.MonitorCount, input.IdempotencyKey, input.PriceVersion)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 201, map[string]any{"order": order})
}
func (s *Server) simulate(w http.ResponseWriter, r *http.Request) {
	account, ok := owner(w, r)
	if !ok {
		return
	}
	var input struct {
		Outcome string `json:"outcome"`
	}
	if !decode(w, r, &input) {
		return
	}
	order, err := s.service.Simulate(r.Context(), account, r.PathValue("id"), input.Outcome)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"order": order})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	account, ok := owner(w, r)
	if !ok {
		return
	}
	order, err := s.service.GetOrder(r.Context(), account, r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"order": order})
}
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, 415, "json_required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	var notification contracts.ProviderNotification
	if decoder.Decode(&notification) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		fail(w, 400, "invalid_request")
		return
	}
	if err = s.service.ReceiveNotification(r.Context(), notification); err != nil {
		if errors.Is(err, service.ErrInvalid) {
			fail(w, 400, "invalid_request")
		} else {
			fail(w, 503, "temporarily_unavailable")
		}
		return
	}
	write(w, 200, map[string]bool{"ok": true})
}

func (s *Server) pricing(w http.ResponseWriter, r *http.Request) {
	pricing, err := s.service.Pricing(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"pricing": pricing})
}
