package notifications

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	auth "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
)

func messengerWrite(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func messengerFailure(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, "temporarily_unavailable"
	switch {
	case errors.Is(err, auth.ErrEmailUnverified):
		status, code = 409, "email_not_verified"
	case errors.Is(err, ErrTelegramPrivateRequired):
		status, code = 409, "telegram_private_required"
	case errors.Is(err, ErrMessengerInvalid):
		status, code = 400, "invalid_request"
	case errors.Is(err, ErrMessengerNotFound):
		status, code = 404, "not_found"
	case errors.Is(err, ErrMessengerConflict):
		status, code = 409, "version_conflict"
	case errors.Is(err, ErrMessengerNotReady):
		status, code = 409, "link_not_ready"
	}
	messengerWrite(w, status, map[string]string{"error": code})
}
func messengerOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		messengerWrite(w, 401, map[string]string{"error": "unauthorized"})
		return "", false
	}
	return p.AccountID, true
}
func messengerDecode(w http.ResponseWriter, r *http.Request, value any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		messengerWrite(w, 415, map[string]string{"error": "json_required"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		messengerFailure(w, ErrMessengerInvalid)
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		messengerFailure(w, ErrMessengerInvalid)
		return false
	}
	return true
}

func (m *Messenger) RegisterAPI(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	for pattern, handler := range map[string]http.HandlerFunc{
		"POST /api/v1/messenger-destinations/email":             m.connectEmail,
		"POST /api/v1/messenger-links":                          m.createLink,
		"GET /api/v1/messenger-links/{id}":                      m.getLink,
		"POST /api/v1/messenger-links/{id}/confirm":             m.confirmLink,
		"GET /api/v1/messenger-destinations":                    m.destinations,
		"DELETE /api/v1/messenger-destinations/{id}":            m.disconnect,
		"GET /api/v1/monitors/{id}/messenger-routes":            m.routes,
		"PUT /api/v1/monitors/{id}/messenger-routes/{provider}": m.putRoute,
	} {
		mux.Handle(pattern, protect(handler))
	}
}

func (m *Messenger) createLink(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	var in struct {
		Provider string `json:"provider"`
		Kind     string `json:"kind"`
	}
	if !messengerDecode(w, r, &in) {
		return
	}
	link, token, url, command, err := m.CreateLink(r.Context(), owner, in.Provider, in.Kind)
	if err != nil {
		messengerFailure(w, err)
		return
	}
	var startURL, commandValue *string
	if url != "" {
		startURL = &url
	}
	if command != "" {
		commandValue = &command
	}
	messengerWrite(w, 201, map[string]any{"link": link, "token": token, "startUrl": startURL, "command": commandValue})
}
func (m *Messenger) getLink(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	link, err := m.GetLink(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		messengerFailure(w, err)
		return
	}
	messengerWrite(w, 200, map[string]any{"link": link})
}
func (m *Messenger) confirmLink(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	d, err := m.ConfirmLink(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		messengerFailure(w, err)
		return
	}
	messengerWrite(w, 200, map[string]any{"destination": d})
}
func (m *Messenger) destinations(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	if !m.cfg.Enabled && !m.cfg.EmailEnabled {
		messengerWrite(w, 200, map[string]any{"enabled": false, "emailEnabled": false, "destinations": []MessengerDestination{}})
		return
	}
	items, err := m.ListDestinations(r.Context(), owner)
	if err != nil {
		messengerFailure(w, err)
		return
	}
	messengerWrite(w, 200, map[string]any{"enabled": m.cfg.Enabled, "emailEnabled": m.cfg.EmailEnabled, "destinations": items})
}
func (m *Messenger) disconnect(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	if err := m.Disconnect(r.Context(), owner, r.PathValue("id")); err != nil {
		messengerFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(204)
}
func (m *Messenger) routes(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	if !m.cfg.Enabled && !m.cfg.EmailEnabled {
		messengerWrite(w, 200, map[string]any{"enabled": false, "emailEnabled": false, "routes": []MessengerRoute{}})
		return
	}
	items, err := m.ListRoutes(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		messengerFailure(w, err)
		return
	}
	messengerWrite(w, 200, map[string]any{"enabled": m.cfg.Enabled, "emailEnabled": m.cfg.EmailEnabled, "routes": items})
}
func (m *Messenger) putRoute(w http.ResponseWriter, r *http.Request) {
	owner, ok := messengerOwner(w, r)
	if !ok {
		return
	}
	var in PutMessengerRoute
	if !messengerDecode(w, r, &in) {
		return
	}
	route, err := m.PutRoute(r.Context(), owner, r.PathValue("id"), r.PathValue("provider"), in)
	if err != nil {
		messengerFailure(w, err)
		return
	}
	messengerWrite(w, 200, map[string]any{"route": route})
}
