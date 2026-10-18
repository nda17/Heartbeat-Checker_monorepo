package notificationhttp

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	auth "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications/internal/service"
)

type Server struct{ service *service.Service }

func New(s *service.Service) *Server { return &Server{service: s} }

func (s *Server) Register(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/monitors/{id}/webhook":            s.get,
		"PUT /api/v1/monitors/{id}/webhook":            s.put,
		"DELETE /api/v1/monitors/{id}/webhook":         s.remove,
		"GET /api/v1/monitors/{id}/webhook/deliveries": s.list,
	} {
		mux.Handle(pattern, protect(handler))
	}
}

func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func fail(w http.ResponseWriter, status int, code string) {
	write(w, status, map[string]string{"error": code})
}
func failure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalid):
		fail(w, 400, "invalid_request")
	case errors.Is(err, service.ErrNotFound):
		fail(w, 404, "not_found")
	case errors.Is(err, service.ErrConflict):
		fail(w, 409, "version_conflict")
	default:
		fail(w, 503, "temporarily_unavailable")
	}
}
func subject(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		fail(w, 401, "unauthorized")
		return "", "", false
	}
	id := r.PathValue("id")
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		fail(w, 404, "not_found")
		return "", "", false
	}
	if _, err := hex.DecodeString(strings.ReplaceAll(id, "-", "")); err != nil {
		fail(w, 404, "not_found")
		return "", "", false
	}
	return p.AccountID, id, true
}
func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	owner, id, ok := subject(w, r)
	if !ok {
		return
	}
	item, err := s.service.Get(r.Context(), owner, id)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"webhook": item})
}
func (s *Server) put(w http.ResponseWriter, r *http.Request) {
	owner, id, ok := subject(w, r)
	if !ok {
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, 415, "json_required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input contracts.PutWebhook
	if err = decoder.Decode(&input); err != nil {
		fail(w, 400, "invalid_request")
		return
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		fail(w, 400, "invalid_request")
		return
	}
	item, secret, err := s.service.Put(r.Context(), owner, id, input)
	if err != nil {
		failure(w, err)
		return
	}
	response := map[string]any{"webhook": item}
	if secret != "" {
		response["secret"] = secret
	}
	write(w, 200, response)
}
func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	owner, id, ok := subject(w, r)
	if !ok {
		return
	}
	if err := s.service.Delete(r.Context(), owner, id); err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(204)
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	owner, id, ok := subject(w, r)
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			fail(w, 400, "invalid_request")
			return
		}
	}
	before := time.Now().Add(time.Second)
	if raw := r.URL.Query().Get("before"); raw != "" {
		var err error
		before, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			fail(w, 400, "invalid_request")
			return
		}
	}
	items, err := s.service.List(r.Context(), owner, id, limit, before)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"deliveries": items})
}
