package admin

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	auth "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors"
	monitorcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
)

func (m *Module) Register(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/admin/accounts":                                                  m.listAccounts,
		"GET /api/v1/admin/accounts/{id}":                                             m.getAccount,
		"PATCH /api/v1/admin/accounts/{id}":                                           m.patchAccount,
		"POST /api/v1/admin/accounts/{id}/revoke-sessions":                            m.revokeSessions,
		"PUT /api/v1/admin/accounts/{id}/entitlement":                                 m.grant,
		"DELETE /api/v1/admin/accounts/{id}/entitlement":                              m.revokeGrant,
		"GET /api/v1/admin/accounts/{id}/monitors":                                    m.listMonitors,
		"POST /api/v1/admin/accounts/{id}/monitors":                                   m.createMonitor,
		"GET /api/v1/admin/accounts/{id}/monitors/{monitorId}":                        m.getMonitor,
		"PATCH /api/v1/admin/accounts/{id}/monitors/{monitorId}":                      m.patchMonitor,
		"DELETE /api/v1/admin/accounts/{id}/monitors/{monitorId}":                     m.deleteMonitor,
		"GET /api/v1/admin/accounts/{id}/monitors/{monitorId}/results":                m.results,
		"GET /api/v1/admin/accounts/{id}/monitors/{monitorId}/incidents":              m.incidents,
		"GET /api/v1/admin/accounts/{id}/monitors/{monitorId}/incidents/{incidentId}": m.incident,
		"GET /api/v1/admin/audit":                                                     m.auditList,
	}
	for pattern, handler := range routes {
		mux.Handle(pattern, protect(handler))
	}
}

func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	if value == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, err error) {
	status, code := 503, "temporarily_unavailable"
	switch {
	case errors.Is(err, ErrInvalid), errors.Is(err, accounts.ErrInvalidName), errors.Is(err, monitors.ErrInvalid):
		status, code = 400, "invalid_request"
	case errors.Is(err, ErrNotFound), errors.Is(err, monitors.ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		status, code = 404, "not_found"
	case errors.Is(err, ErrConflict), errors.Is(err, monitors.ErrConflict):
		status, code = 409, "version_conflict"
	case errors.Is(err, ErrForbidden):
		status, code = 403, "forbidden"
	case errors.Is(err, monitors.ErrEntitlement):
		status, code = 403, "entitlement_required"
	case errors.Is(err, monitors.ErrQuota):
		status, code = 403, "quota_exceeded"
	}
	write(w, status, map[string]string{"error": code})
}

func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		write(w, 415, map[string]string{"error": "json_required"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		write(w, 400, map[string]string{"error": "invalid_request"})
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		write(w, 400, map[string]string{"error": "invalid_request"})
		return false
	}
	return true
}

func principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, ok := auth.FromContext(r.Context())
	if !ok || p.Role != "admin" {
		write(w, 403, map[string]string{"error": "forbidden"})
		return auth.Principal{}, false
	}
	return p, true
}

func paging(r *http.Request) (int, string, error) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			return 0, "", ErrInvalid
		}
		limit = value
	}
	return limit, r.URL.Query().Get("cursor"), nil
}

func (m *Module) listAccounts(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := paging(r)
	if err != nil {
		fail(w, err)
		return
	}
	items, next, err := m.ListAccounts(r.Context(), r.URL.Query().Get("q"), cursor, limit)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "nextCursor": next})
}
func (m *Module) getAccount(w http.ResponseWriter, r *http.Request) {
	item, err := m.GetAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, item)
}
func (m *Module) patchAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in AccountPatch
	if !decode(w, r, &in) {
		return
	}
	item, err := m.PatchAccount(r.Context(), p, r.PathValue("id"), in)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, item)
}
func (m *Module) revokeSessions(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := m.RevokeSessions(r.Context(), p, r.PathValue("id"), in.Reason); err != nil {
		fail(w, err)
		return
	}
	write(w, 204, nil)
}
func (m *Module) grant(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in GrantInput
	if !decode(w, r, &in) {
		return
	}
	e, err := m.Grant(r.Context(), p, r.PathValue("id"), in)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]any{"entitlement": e})
}
func (m *Module) revokeGrant(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in struct {
		Version int64  `json:"version"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := m.RevokeGrant(r.Context(), p, r.PathValue("id"), in.Version, in.Reason); err != nil {
		fail(w, err)
		return
	}
	write(w, 204, nil)
}
func (m *Module) listMonitors(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListMonitors(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]any{"monitors": items})
}
func (m *Module) getMonitor(w http.ResponseWriter, r *http.Request) {
	item, err := m.GetMonitor(r.Context(), r.PathValue("id"), r.PathValue("monitorId"))
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]any{"monitor": item})
}
func (m *Module) createMonitor(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in struct {
		monitorcontracts.MonitorInput
		Reason string `json:"reason"`
	}
	in.Enabled = true
	if !decode(w, r, &in) {
		return
	}
	item, err := m.CreateMonitor(r.Context(), p, r.PathValue("id"), in.Reason, in.MonitorInput)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 201, map[string]any{"monitor": item})
}
func (m *Module) patchMonitor(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in struct {
		monitorcontracts.Patch
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	item, err := m.PatchMonitor(r.Context(), p, r.PathValue("id"), r.PathValue("monitorId"), in.Reason, in.Patch)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]any{"monitor": item})
}
func (m *Module) deleteMonitor(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in struct {
		Version int64  `json:"version"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := m.DeleteMonitor(r.Context(), p, r.PathValue("id"), r.PathValue("monitorId"), in.Version, in.Reason); err != nil {
		fail(w, err)
		return
	}
	write(w, 204, nil)
}
func (m *Module) results(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			fail(w, ErrInvalid)
			return
		}
		limit = n
	}
	before := time.Now().Add(time.Second)
	if raw := r.URL.Query().Get("before"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			fail(w, ErrInvalid)
			return
		}
		before = parsed
	}
	items, err := m.Results(r.Context(), r.PathValue("id"), r.PathValue("monitorId"), limit, before)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]any{"results": items})
}
func (m *Module) incidents(w http.ResponseWriter, r *http.Request) {
	items, err := m.Incidents(r.Context(), r.PathValue("id"), r.PathValue("monitorId"))
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]any{"incidents": items})
}
func (m *Module) incident(w http.ResponseWriter, r *http.Request) {
	item, err := m.Incident(r.Context(), r.PathValue("id"), r.PathValue("monitorId"), r.PathValue("incidentId"))
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]any{"incident": item})
}
func (m *Module) auditList(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := paging(r)
	if err != nil {
		fail(w, err)
		return
	}
	items, next, err := m.Audit(r.Context(), r.URL.Query().Get("accountId"), cursor, limit)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "nextCursor": next})
}
