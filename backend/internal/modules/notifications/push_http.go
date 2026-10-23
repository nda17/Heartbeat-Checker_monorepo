package notifications

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	case errors.Is(err, ErrPushAccountChange):
		status, code = 409, "push_account_change_required"
	case errors.Is(err, ErrPushReconnect):
		status, code = 409, pushSessionStatus(err)
	case errors.Is(err, ErrPushRateLimited):
		status, code = 429, "rate_limited"
		w.Header().Set("Retry-After", "60")
		var testLimit pushTestRateError
		if errors.As(err, &testLimit) {
			seconds := max(1, int(time.Until(testLimit.Next).Seconds()+0.999999999))
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			messengerWrite(w, status, map[string]any{"error": code, "nextAllowedAt": testLimit.Next})
			return
		}
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
		"POST /api/v1/push/native/bind":           p.bindNativePush,
		"GET /api/v1/push/native/installation":    p.getNativePushInstallation,
		"POST /api/v1/push/native/tests":          p.createNativePushTest,
		"GET /api/v1/push/native/tests/{id}":      p.getNativePushTest,
		"GET /api/v1/push/installations":          p.listPushInstallations,
		"DELETE /api/v1/push/installations/{id}":  p.disconnectPushInstallation,
		"GET /api/v1/push/pairings/{id}":          p.getPushPairing,
		"POST /api/v1/push/pairings/{id}/confirm": p.confirmPushPairing,
		"GET /api/v1/monitors/{id}/push":          p.getMonitorPush,
		"PUT /api/v1/monitors/{id}/push":          p.putMonitorPush,
	} {
		mux.Handle(pattern, protect(handler))
	}
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/push/capabilities":                               p.pushCapabilities,
		"POST /api/v1/push/pairings":                                  p.createPushPairing,
		"POST /api/v1/push/pairings/{id}/exchange":                    p.exchangePushPairing,
		"GET /api/v1/push/installation":                               p.getPushInstallation,
		"PUT /api/v1/push/installation":                               p.updatePushInstallation,
		"DELETE /api/v1/push/installation":                            p.revokePushInstallation,
		"GET /api/v1/push/installation/deliveries/{deliveryId}":       p.getPushDelivery,
		"GET /api/v1/push/installation/test-deliveries/{id}":          p.getInstallationPushTest,
		"POST /api/v1/push/installation/test-deliveries/{id}/receipt": p.receiptInstallationPushTest,
	} {
		mux.Handle(pattern, handler)
	}
}

// Capabilities are public project-level transport availability, never credentials.
func (p *Push) pushCapabilities(w http.ResponseWriter, r *http.Request) {
	providers := []string{}
	for _, provider := range []string{"fcm", "huawei", "rustore"} {
		if p.providerEnabled(provider) {
			providers = append(providers, provider)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	messengerWrite(w, 200, map[string]any{"protocolVersion": 2, "providers": providers})
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
	messengerWrite(w, 201, map[string]any{"pairing": map[string]any{"id": result.Pairing.ID, "expiresAt": result.Pairing.ExpiresAt, "confirmationUrl": result.Pairing.ConfirmationURL}, "installation": result.Installation})
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
	messengerWrite(w, 200, map[string]any{"id": pair.ID, "deviceLabel": pair.DeviceLabel, "status": pair.Status, "expiresAt": pair.ExpiresAt})
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
	result, err := p.ConfirmPairing(r.Context(), principal.AccountID, principal.SessionID, r.PathValue("id"))
	if err != nil {
		pushFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	messengerWrite(w, 200, result)
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
	if in.Provider != "rustore" && r.Header.Get("X-Heartbeat-Push-Protocol") != "2" {
		pushFailure(w, ErrPushInvalid)
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
	if result.Provider != "rustore" && r.Header.Get("X-Heartbeat-Push-Protocol") != "2" {
		pushFailure(w, ErrPushReconnect)
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
	if in.Provider != "" && in.Provider != "rustore" && r.Header.Get("X-Heartbeat-Push-Protocol") != "2" {
		pushFailure(w, ErrPushInvalid)
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
func (p *Push) getMonitorPush(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	result, err := p.MonitorPush(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		pushFailure(w, err)
		return
	}
	messengerWrite(w, 200, result)
}
func (p *Push) putMonitorPush(w http.ResponseWriter, r *http.Request) {
	principal, ok := authcontracts.FromContext(r.Context())
	if !ok {
		pushFailure(w, ErrPushUnauthorized)
		return
	}
	var in PutPushMonitorSettings
	if !pushDecode(w, r, &in) {
		return
	}
	result, err := p.PutMonitorPush(r.Context(), principal.AccountID, principal.SessionID, r.PathValue("id"), in)
	if err != nil {
		pushFailure(w, err)
		return
	}
	messengerWrite(w, 200, result)
}
