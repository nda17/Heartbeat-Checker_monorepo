package notifications

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"

	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
)

func pushFailure(w http.ResponseWriter, err error) {
	status, code := 503, "temporarily_unavailable"
	switch {
	case errors.Is(err, ErrPushInvalid):
		status, code = 400, "invalid_request"
	case errors.Is(err, ErrPushUnauthorized):
		status, code = 401, "unauthorized"
	case errors.Is(err, ErrPushNotFound):
		status, code = 404, "not_found"
	case errors.Is(err, ErrPushPairingConsumed):
		status, code = 409, "pairing_consumed"
	case errors.Is(err, ErrPushConflict):
		status, code = 409, "version_conflict"
	case errors.Is(err, ErrPushReconnect):
		status, code = 409, pushSessionStatus(err)
	case errors.Is(err, ErrPushRateLimited):
		status, code = 429, "rate_limited"
		w.Header().Set("Retry-After", "60")
	}
	messengerWrite(w, status, map[string]string{"error": code})
}

func pushDecode(w http.ResponseWriter, r *http.Request, value any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		messengerWrite(w, 415, map[string]string{"error": "json_required"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		pushFailure(w, ErrPushInvalid)
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		pushFailure(w, ErrPushInvalid)
		return false
	}
	return true
}

func pushBearer(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(header, "Bearer ")
}

func (p *Push) RegisterAPI(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/push/installations":                         p.listPushInstallations,
		"DELETE /api/v1/push/installations/{id}":                 p.disconnectPushInstallation,
		"GET /api/v1/push/pairings/{id}":                         p.getPushPairing,
		"POST /api/v1/push/pairings/{id}/confirm":                p.confirmPushPairing,
		"GET /api/v1/monitors/{id}/push-routes":                  p.listPushRoutes,
		"PUT /api/v1/monitors/{id}/push-routes/{installationId}": p.putPushRoute,
	} {
		mux.Handle(pattern, protect(handler))
	}
	for pattern, handler := range map[string]http.HandlerFunc{
		"POST /api/v1/push/pairings":                            p.createPushPairing,
		"POST /api/v1/push/pairings/{id}/exchange":              p.exchangePushPairing,
		"GET /api/v1/push/installation":                         p.getPushInstallation,
		"PUT /api/v1/push/installation":                         p.updatePushInstallation,
		"DELETE /api/v1/push/installation":                      p.revokePushInstallation,
		"GET /api/v1/push/installation/deliveries/{deliveryId}": p.getPushDelivery,
	} {
		mux.Handle(pattern, handler)
	}
}

func (p *Push) nativeCredential(w http.ResponseWriter, r *http.Request) (string, bool) {
	bearer := pushBearer(r)
	if err := p.rateInstallation(r.Context(), bearer); err != nil {
		pushFailure(w, err)
		return "", false
	}
	return bearer, true
}
func (p *Push) createPushPairing(w http.ResponseWriter, r *http.Request) {
	var in PushPairingInput
	if !pushDecode(w, r, &in) {
		return
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		pushFailure(w, ErrPushInvalid)
		return
	}
	bearer := pushBearer(r)
	if r.Header.Get("Authorization") != "" && bearer == "" {
		pushFailure(w, ErrPushUnauthorized)
		return
	}
	result, err := p.CreatePairing(r.Context(), bearer, ip, in)
	if err != nil {
		pushFailure(w, err)
		return
	}
	messengerWrite(w, 201, map[string]any{"pairing": map[string]any{"id": result.Pairing.ID, "expiresAt": result.Pairing.ExpiresAt, "verificationCode": result.Pairing.VerificationCode, "confirmationUrl": result.Pairing.ConfirmationURL}, "installation": result.Installation})
}
func (p *Push) getPushPairing(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	pair, err := p.Pairing(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		pushFailure(w, err)
		return
	}
	messengerWrite(w, 200, map[string]any{"id": pair.ID, "deviceLabel": pair.DeviceLabel, "verificationCode": pair.VerificationCode, "status": pair.Status, "expiresAt": pair.ExpiresAt})
}
func (p *Push) confirmPushPairing(w http.ResponseWriter, r *http.Request) {
	principal, ok := authcontracts.FromContext(r.Context())
	if !ok {
		pushFailure(w, ErrPushUnauthorized)
		return
	}
	var in struct{}
	if !pushDecode(w, r, &in) {
		return
	}
	if err := p.ConfirmPairing(r.Context(), principal.AccountID, principal.SessionID, r.PathValue("id")); err != nil {
		pushFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(204)
}
func (p *Push) exchangePushPairing(w http.ResponseWriter, r *http.Request) {
	bearer, ok := p.nativeCredential(w, r)
	if !ok {
		return
	}
	var in PushExchangeInput
	if !pushDecode(w, r, &in) {
		return
	}
	result, err := p.ExchangePairing(r.Context(), bearer, r.PathValue("id"), in)
	if err != nil {
		pushFailure(w, err)
		return
	}
	messengerWrite(w, 200, result)
}
func (p *Push) getPushInstallation(w http.ResponseWriter, r *http.Request) {
	bearer, ok := p.nativeCredential(w, r)
	if !ok {
		return
	}
	result, err := p.Installation(r.Context(), bearer)
	if err != nil {
		pushFailure(w, err)
		return
	}
	messengerWrite(w, 200, result)
}
func (p *Push) updatePushInstallation(w http.ResponseWriter, r *http.Request) {
	bearer, ok := p.nativeCredential(w, r)
	if !ok {
		return
	}
	var in PushRegistrationInput
	if !pushDecode(w, r, &in) {
		return
	}
	result, err := p.UpdateRegistration(r.Context(), bearer, in)
	if err != nil {
		pushFailure(w, err)
		return
	}
	messengerWrite(w, 200, result)
}
func (p *Push) revokePushInstallation(w http.ResponseWriter, r *http.Request) {
	bearer, ok := p.nativeCredential(w, r)
	if !ok {
		return
	}
	if err := p.RevokeInstallation(r.Context(), bearer); err != nil {
		pushFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(204)
}
func (p *Push) getPushDelivery(w http.ResponseWriter, r *http.Request) {
	bearer, ok := p.nativeCredential(w, r)
	if !ok {
		return
	}
	result, err := p.DeliveryContent(r.Context(), bearer, r.PathValue("deliveryId"))
	if err != nil {
		pushFailure(w, err)
		return
	}
	messengerWrite(w, 200, result)
}
func (p *Push) listPushInstallations(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	items, err := p.ListInstallations(r.Context(), owner)
	if err != nil {
		pushFailure(w, err)
		return
	}
	messengerWrite(w, 200, map[string]any{"enabled": p.cfg.Enabled, "installations": items})
}
func (p *Push) disconnectPushInstallation(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	if err := p.DisconnectInstallation(r.Context(), owner, r.PathValue("id")); err != nil {
		pushFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(204)
}
func (p *Push) listPushRoutes(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	items, err := p.ListPushRoutes(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		pushFailure(w, err)
		return
	}
	messengerWrite(w, 200, map[string]any{"routes": items})
}
func (p *Push) putPushRoute(w http.ResponseWriter, r *http.Request) {
	principal, ok := authcontracts.FromContext(r.Context())
	if !ok {
		pushFailure(w, ErrPushUnauthorized)
		return
	}
	var in PutPushRoute
	if !pushDecode(w, r, &in) {
		return
	}
	result, err := p.PutPushRoute(r.Context(), principal.AccountID, principal.SessionID, r.PathValue("id"), r.PathValue("installationId"), in)
	if err != nil {
		pushFailure(w, err)
		return
	}
	messengerWrite(w, 200, map[string]any{"route": result})
}
