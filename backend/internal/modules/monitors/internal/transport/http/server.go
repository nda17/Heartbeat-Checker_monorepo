package monitorhttp

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	auth "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/internal/service"
)

type Server struct{ service *service.Service }

func New(s *service.Service) *Server { return &Server{s} }
func (s *Server) Register(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/monitors": s.list, "POST /api/v1/monitors": s.create,
		"GET /api/v1/monitors/{id}": s.get, "PATCH /api/v1/monitors/{id}": s.patch, "DELETE /api/v1/monitors/{id}": s.remove,
		"GET /api/v1/monitors/{id}/results": s.results, "GET /api/v1/monitors/{id}/incidents": s.incidents,
		"GET /api/v1/monitors/{id}/incidents/{incidentId}": s.incident,
		"GET /api/v1/entitlement":                          s.entitlement,
	}
	for pattern, handler := range routes {
		mux.Handle(pattern, protect(handler))
	}
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
	case errors.Is(err, service.ErrInvalid):
		fail(w, 400, "invalid_request")
	case errors.Is(err, service.ErrNotFound):
		fail(w, 404, "not_found")
	case errors.Is(err, service.ErrConflict):
		fail(w, 409, "version_conflict")
	case errors.Is(err, service.ErrEntitlement):
		fail(w, 403, "entitlement_required")
	case errors.Is(err, service.ErrQuota):
		fail(w, 403, "quota_exceeded")
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
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		fail(w, 400, "invalid_request")
		return false
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		fail(w, 400, "invalid_request")
		return false
	}
	return true
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	id, ok := owner(w, r)
	if !ok {
		return
	}
	options, paged, err := listOptions(r.URL.RawQuery)
	if err != nil {
		failure(w, err)
		return
	}
	if paged {
		result, err := s.service.ListPage(r.Context(), id, options)
		if err != nil {
			failure(w, err)
			return
		}
		write(w, 200, result)
		return
	}
	items, err := s.service.List(r.Context(), id)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"monitors": items})
}

// No list parameters preserves the legacy response and oldest-first order.
func listOptions(raw string) (contracts.ListOptions, bool, error) {
	options := contracts.ListOptions{Page: 1, PageSize: 25, Sort: "created_desc"}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return options, false, service.ErrInvalid
	}
	if len(values) == 0 {
		return options, false, nil
	}
	for key, items := range values {
		if len(items) != 1 {
			return options, true, service.ErrInvalid
		}
		switch key {
		case "page", "pageSize":
			value := items[0]
			if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) != -1 {
				return options, true, service.ErrInvalid
			}
			number, err := strconv.Atoi(value)
			if err != nil || number < 1 || (key == "pageSize" && number > 100) {
				return options, true, service.ErrInvalid
			}
			if key == "page" {
				options.Page = number
			} else {
				options.PageSize = number
			}
		case "sort":
			switch items[0] {
			case "created_desc", "created_asc", "name_asc", "name_desc":
				options.Sort = items[0]
			default:
				return options, true, service.ErrInvalid
			}
		case "q":
			options.Query = strings.TrimSpace(items[0])
			if !utf8.ValidString(options.Query) || utf8.RuneCountInString(options.Query) > 200 {
				return options, true, service.ErrInvalid
			}
		default:
			return options, true, service.ErrInvalid
		}
	}
	return options, true, nil
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	id, ok := owner(w, r)
	if !ok {
		return
	}
	m, err := s.service.Get(r.Context(), id, r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"monitor": m})
}
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	id, ok := owner(w, r)
	if !ok {
		return
	}
	in := struct {
		contracts.MonitorInput
		ExpectedAccountID *string `json:"expectedAccountId,omitempty"`
	}{MonitorInput: contracts.MonitorInput{Enabled: true}}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedAccountID != nil {
		var expected pgtype.UUID
		if len(*in.ExpectedAccountID) != 36 || expected.Scan(*in.ExpectedAccountID) != nil || !expected.Valid || expected.String() != *in.ExpectedAccountID {
			fail(w, 400, "invalid_request")
			return
		}
		if *in.ExpectedAccountID != id {
			fail(w, 403, "account_changed")
			return
		}
	}
	m, err := s.service.Create(r.Context(), id, in.MonitorInput)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 201, map[string]any{"monitor": m})
}
func (s *Server) patch(w http.ResponseWriter, r *http.Request) {
	id, ok := owner(w, r)
	if !ok {
		return
	}
	var in contracts.Patch
	if !decode(w, r, &in) {
		return
	}
	m, err := s.service.Patch(r.Context(), id, r.PathValue("id"), in)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"monitor": m})
}
func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	id, ok := owner(w, r)
	if !ok {
		return
	}
	if err := s.service.Delete(r.Context(), id, r.PathValue("id")); err != nil {
		failure(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) results(w http.ResponseWriter, r *http.Request) {
	id, ok := owner(w, r)
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			fail(w, 400, "invalid_request")
			return
		}
		limit = n
	}
	before := time.Now().Add(time.Second)
	if raw := r.URL.Query().Get("before"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			fail(w, 400, "invalid_request")
			return
		}
		before = parsed
	}
	items, err := s.service.Results(r.Context(), id, r.PathValue("id"), limit, before)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"results": items})
}
func (s *Server) incidents(w http.ResponseWriter, r *http.Request) {
	id, ok := owner(w, r)
	if !ok {
		return
	}
	items, err := s.service.Incidents(r.Context(), id, r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"incidents": items})
}
func (s *Server) incident(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := owner(w, r)
	if !ok {
		return
	}
	i, err := s.service.Incident(r.Context(), ownerID, r.PathValue("id"), r.PathValue("incidentId"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"incident": i})
}
func (s *Server) entitlement(w http.ResponseWriter, r *http.Request) {
	id, ok := owner(w, r)
	if !ok {
		return
	}
	e, err := s.service.Entitlement(r.Context(), id)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"entitlement": e})
}
