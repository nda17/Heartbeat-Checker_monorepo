package authhttp

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/service"
)

func (s *Server) oauthCookieName(kind string) string {
	if s.secure {
		return "__Host-hc_oauth_" + kind
	}
	return "hc_oauth_" + kind
}
func (s *Server) oauthCookie(r *http.Request, kind string) string {
	c, err := r.Cookie(s.oauthCookieName(kind))
	if err != nil {
		return ""
	}
	return c.Value
}
func (s *Server) setOAuthCookie(w http.ResponseWriter, kind, value string, ttl time.Duration) {
	c := &http.Cookie{Name: s.oauthCookieName(kind), Value: value, Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(ttl)}
	if value == "" {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}
func (s *Server) sessionToken(r *http.Request) string {
	c, err := r.Cookie(s.cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
func (s *Server) oauthProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"providers": s.service.OAuthProviders()})
}
func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(w, r) {
		return
	}
	var in struct {
		Mode     string `json:"mode"`
		ReturnTo string `json:"returnTo"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	input := service.OAuthStartInput{Provider: r.PathValue("provider"), Mode: in.Mode, ReturnTo: in.ReturnTo, Password: in.Password, PreviousCookie: s.oauthCookie(r, "attempt"), SessionToken: s.sessionToken(r), Client: client(r)}
	if in.Mode == "link" {
		p, token, ok := s.require(w, r, true)
		if !ok {
			return
		}
		input.Principal = &p
		input.SessionToken = token
	} else if input.SessionToken != "" {
		p, err := s.service.Authenticate(r.Context(), input.SessionToken)
		if err == nil {
			input.Principal = &p
		} else if !errors.Is(err, service.ErrUnauthorized) {
			failService(w, err)
			return
		}
	}
	out, err := s.service.OAuthStart(r.Context(), input)
	if err != nil {
		failService(w, err)
		return
	}
	s.setOAuthCookie(w, "attempt", out.Cookie, 10*time.Minute)
	s.setOAuthCookie(w, "mode", in.Mode, 10*time.Minute)
	writeJSON(w, 200, out)
}
func oauthErrorCode(err error) string {
	switch {
	case errors.Is(err, service.ErrOAuthCancelled):
		return "oauth_cancelled"
	case errors.Is(err, service.ErrOAuthEmailConflict):
		return "oauth_email_conflict"
	case errors.Is(err, service.ErrOAuthEmailRequired):
		return "oauth_email_required"
	case errors.Is(err, service.ErrOAuthLoginRequiresLogout):
		return "oauth_login_requires_logout"
	case errors.Is(err, service.ErrOAuthIdentityConflict):
		return "oauth_identity_conflict"
	case errors.Is(err, service.ErrOAuthState):
		return "oauth_state_invalid"
	case errors.Is(err, service.ErrMFAEnrollmentRequired):
		return "mfa_enrollment_required"
	case errors.Is(err, service.ErrMFARequired):
		return "mfa_required"
	case errors.Is(err, service.ErrMFAStepUpRequired):
		return "mfa_step_up_required"
	default:
		return "oauth_provider_unavailable"
	}
}

// parseOAuthCallback rejects ambiguous or oversized parameters before touching
// a browser's current attempt. Provider callbacks are query-only GET requests.
func parseOAuthCallback(raw string) (url.Values, error) {
	if len(raw) == 0 || len(raw) > 16<<10 {
		return nil, service.ErrOAuthState
	}
	q, err := url.ParseQuery(raw)
	if err != nil || len(q) > 24 {
		return nil, service.ErrOAuthState
	}
	for key, values := range q {
		if len(key) == 0 || len(key) > 64 || len(values) != 1 {
			return nil, service.ErrOAuthState
		}
		limit := 2048
		switch key {
		case "code":
			limit = 8192
		case "state":
			limit = 43
		case "device_id":
			limit = 4096
		case "error":
			limit = 128
		case "error_description":
			limit = 1024
		}
		if len(values[0]) > limit || !utf8.ValidString(key+values[0]) || strings.ContainsFunc(key+values[0], func(r rune) bool { return r < 32 || r == 127 }) {
			return nil, service.ErrOAuthState
		}
	}
	if len(q.Get("state")) != 43 {
		return nil, service.ErrOAuthState
	}
	_, hasCode := q["code"]
	_, hasError := q["error"]
	if hasCode == hasError || hasCode && q.Get("code") == "" || hasError && q.Get("error") == "" {
		return nil, service.ErrOAuthState
	}
	return q, nil
}
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// The unsigned mode cookie only chooses a local error page. Database state
	// remains authoritative for authorization and for consumed attempts.
	errorDestination := "/login"
	if s.oauthCookie(r, "mode") == "link" {
		errorDestination = "/app/settings"
	}
	q, parseErr := parseOAuthCallback(r.URL.RawQuery)
	if parseErr != nil {
		http.Redirect(w, r, s.origin+errorDestination+"?oauthError=oauth_state_invalid", http.StatusSeeOther)
		return
	}
	input := service.OAuthCallbackInput{Provider: r.PathValue("provider"), Code: q.Get("code"), State: q.Get("state"), DeviceID: q.Get("device_id"), ProviderError: q.Get("error"), Cookie: s.oauthCookie(r, "attempt"), SessionToken: s.sessionToken(r), Client: client(r)}
	out, err := s.service.OAuthCallback(r.Context(), input)
	if out.Consumed {
		s.setOAuthCookie(w, "attempt", "", 0)
		s.setOAuthCookie(w, "mode", "", 0)
	}
	if err != nil {
		destination := errorDestination
		if out.Consumed {
			destination = "/login"
			if out.Mode == "link" {
				destination = "/app/settings"
			}
		}
		query := url.Values{"oauthError": {oauthErrorCode(err)}}
		if out.Consumed && out.Mode == "login" && out.RetryReturnTo != "" {
			query.Set("returnTo", out.RetryReturnTo)
		}
		http.Redirect(w, r, s.origin+destination+"?"+query.Encode(), http.StatusSeeOther)
		return
	}
	if out.Login != nil {
		s.cookie(w, out.Login.Token)
	}
	if out.RegistrationCookie != "" {
		s.setOAuthCookie(w, "registration", out.RegistrationCookie, 24*time.Hour)
	}
	http.Redirect(w, r, s.origin+out.ReturnTo, http.StatusSeeOther)
}
func (s *Server) oauthRegistration(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		fail(w, 403, "forbidden")
		return
	}
	out, err := s.service.OAuthRegistration(r.Context(), s.oauthCookie(r, "registration"))
	if err != nil {
		failService(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) oauthConfirm(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(w, r) {
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &in) {
		return
	}
	if token := s.sessionToken(r); token != "" {
		_, err := s.service.Authenticate(r.Context(), token)
		if err == nil {
			failService(w, service.ErrOAuthLoginRequiresLogout)
			return
		}
		if !errors.Is(err, service.ErrUnauthorized) {
			failService(w, err)
			return
		}
	}
	out, err := s.service.OAuthConfirm(r.Context(), s.oauthCookie(r, "registration"), in.Token, client(r), s.sessionToken(r))
	if err != nil {
		failService(w, err)
		return
	}
	s.cookie(w, out.Login.Token)
	s.setOAuthCookie(w, "registration", "", 0)
	writeJSON(w, 200, map[string]any{"account": out.Login.Account, "csrfToken": out.Login.CSRF, "returnTo": out.ReturnTo})
}
func (s *Server) oauthIdentities(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.require(w, r, false)
	if !ok {
		return
	}
	out, err := s.service.OAuthIdentities(r.Context(), p)
	if err != nil {
		failService(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"identities": out})
}
func (s *Server) oauthUnlink(w http.ResponseWriter, r *http.Request) {
	p, raw, ok := s.require(w, r, true)
	if !ok {
		return
	}
	var in struct {
		IdentityID string `json:"identityId"`
		Password   string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	token, err := s.service.OAuthUnlink(r.Context(), p, raw, r.PathValue("provider"), in.IdentityID, in.Password, client(r))
	if err != nil {
		failService(w, err)
		return
	}
	s.cookie(w, token)
	w.WriteHeader(http.StatusNoContent)
}
