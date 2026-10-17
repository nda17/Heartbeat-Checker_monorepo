package authhttp

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/service"
)

type Server struct {
	service    *service.Service
	origin     string
	cookieName string
	secure     bool
	ttl        time.Duration
}

func New(s *service.Service, origin string, secure bool, ttl time.Duration) *Server {
	name := "hc_session"
	if secure {
		name = "__Host-hc_session"
	}
	return &Server{service: s, origin: origin, cookieName: name, secure: secure, ttl: ttl}
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/register", s.register)
	mux.HandleFunc("POST /api/v1/auth/verify", s.verify)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("POST /api/v1/auth/password/reset/request", s.resetRequest)
	mux.HandleFunc("POST /api/v1/auth/password/reset/confirm", s.resetConfirm)
	mux.HandleFunc("POST /api/v1/auth/password/change", s.passwordChange)
	mux.HandleFunc("GET /api/v1/auth/csrf", s.csrf)
	mux.HandleFunc("GET /api/v1/me", s.me)
	mux.HandleFunc("PATCH /api/v1/me", s.editMe)
	mux.HandleFunc("GET /api/v1/me/sessions", s.sessions)
	mux.HandleFunc("DELETE /api/v1/me/sessions/{id}", s.revokeSession)
	mux.HandleFunc("DELETE /api/v1/me/sessions", s.revokeAllSessions)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.Register(mux)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
func failService(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalid):
		fail(w, 400, "invalid_request")
	case errors.Is(err, service.ErrCredentials):
		fail(w, 401, "invalid_credentials")
	case errors.Is(err, service.ErrUnauthorized):
		fail(w, 401, "unauthorized")
	case errors.Is(err, service.ErrNotFound):
		fail(w, 404, "not_found")
	case errors.Is(err, service.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "rate_limited")
	case errors.Is(err, service.ErrBusy):
		w.Header().Set("Retry-After", "1")
		fail(w, 503, "temporarily_unavailable")
	case errors.Is(err, service.ErrChallenge):
		fail(w, 403, "challenge_failed")
	default:
		fail(w, 503, "temporarily_unavailable")
	}
}

func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		fail(w, 415, "json_required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		fail(w, 400, "invalid_request")
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		fail(w, 400, "invalid_request")
		return false
	}
	return true
}

func (s *Server) originOK(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Origin") != s.origin || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		fail(w, 403, "forbidden")
		return false
	}
	return true
}

func client(r *http.Request) service.Client {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = ""
	}
	return service.Client{IP: ip, UserAgent: r.UserAgent()}
}

func (s *Server) cookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: s.cookieName, Value: token, Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(s.ttl)})
}
func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: s.cookieName, Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func (s *Server) require(w http.ResponseWriter, r *http.Request, mutation bool) (service.Principal, string, bool) {
	if mutation && !s.originOK(w, r) {
		return service.Principal{}, "", false
	}
	cookie, err := r.Cookie(s.cookieName)
	if err != nil {
		fail(w, 401, "unauthorized")
		return service.Principal{}, "", false
	}
	p, err := s.service.Authenticate(r.Context(), cookie.Value)
	if err != nil {
		failService(w, err)
		return service.Principal{}, "", false
	}
	if mutation {
		expected := security.CSRF(cookie.Value)
		got := r.Header.Get("X-CSRF-Token")
		if len(got) != len(expected) || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			fail(w, 403, "invalid_csrf")
			return service.Principal{}, "", false
		}
	}
	return p, cookie.Value, true
}

// Admin authorization remains closed until MFA is enforced here.
func (s *Server) RequireRole(role string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.require(w, r, r.Method != http.MethodGet && r.Method != http.MethodHead)
		if !ok {
			return
		}
		if role == "admin" || p.Role != role {
			fail(w, 403, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(w, r) {
		return
	}
	var in struct {
		Email          string `json:"email"`
		Name           string `json:"name"`
		Password       string `json:"password"`
		TurnstileToken string `json:"turnstileToken"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.service.Register(r.Context(), in.Email, strings.TrimSpace(in.Name), in.Password, in.TurnstileToken, client(r)); err != nil {
		failService(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(w, r) {
		return
	}
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.service.Verify(r.Context(), in.Token, in.Password, client(r)); err != nil {
		failService(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(w, r) {
		return
	}
	var in struct {
		Email          string `json:"email"`
		Password       string `json:"password"`
		TurnstileToken string `json:"turnstileToken"`
	}
	if !decode(w, r, &in) {
		return
	}
	result, err := s.service.Login(r.Context(), in.Email, in.Password, in.TurnstileToken, client(r))
	if err != nil {
		failService(w, err)
		return
	}
	s.cookie(w, result.Token)
	writeJSON(w, 200, map[string]any{"account": result.Account, "csrfToken": result.CSRF})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.require(w, r, true)
	if !ok {
		return
	}
	if err := s.service.RevokeSession(r.Context(), p, p.SessionID); err != nil {
		failService(w, err)
		return
	}
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) csrf(w http.ResponseWriter, r *http.Request) {
	_, raw, ok := s.require(w, r, false)
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]string{"csrfToken": security.CSRF(raw)})
}
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.require(w, r, false)
	if !ok {
		return
	}
	profile, err := s.service.Profile(r.Context(), p)
	if err != nil {
		failService(w, err)
		return
	}
	writeJSON(w, 200, profile)
}
func (s *Server) editMe(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.require(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	profile, err := s.service.ChangeName(r.Context(), p, strings.TrimSpace(in.Name))
	if err != nil {
		failService(w, err)
		return
	}
	writeJSON(w, 200, profile)
}
func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.require(w, r, false)
	if !ok {
		return
	}
	items, err := s.service.Sessions(r.Context(), p)
	if err != nil {
		failService(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"sessions": items})
}
func (s *Server) revokeSession(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.require(w, r, true)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := s.service.RevokeSession(r.Context(), p, id); err != nil {
		failService(w, err)
		return
	}
	if id == p.SessionID {
		s.clearCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) revokeAllSessions(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.require(w, r, true)
	if !ok {
		return
	}
	if err := s.service.RevokeAllSessions(r.Context(), p); err != nil {
		failService(w, err)
		return
	}
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) resetRequest(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(w, r) {
		return
	}
	var in struct {
		Email          string `json:"email"`
		TurnstileToken string `json:"turnstileToken"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.service.ResetRequest(r.Context(), in.Email, in.TurnstileToken, client(r)); err != nil {
		failService(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
func (s *Server) resetConfirm(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(w, r) {
		return
	}
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.service.ResetConfirm(r.Context(), in.Token, in.Password, client(r)); err != nil {
		failService(w, err)
		return
	}
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) passwordChange(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.require(w, r, true)
	if !ok {
		return
	}
	var in struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.service.ChangePassword(r.Context(), p, in.CurrentPassword, in.NewPassword, client(r)); err != nil {
		failService(w, err)
		return
	}
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
