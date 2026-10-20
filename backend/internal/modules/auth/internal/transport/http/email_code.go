package authhttp

import (
	"errors"
	"net/http"
	"time"

	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/service"
)

func (s *Server) registrationCookieName() string {
	if s.secure {
		return "__Host-hc_registration"
	}
	return "hc_registration"
}
func (s *Server) registrationCookie(r *http.Request) string {
	c, err := r.Cookie(s.registrationCookieName())
	if err != nil {
		return ""
	}
	return c.Value
}
func (s *Server) setRegistrationCookie(w http.ResponseWriter, value string, expires time.Time) {
	c := &http.Cookie{Name: s.registrationCookieName(), Value: value, Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode, Expires: expires}
	if value == "" {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}
func (s *Server) registrationLoggedOut(w http.ResponseWriter, r *http.Request) bool {
	token := s.sessionToken(r)
	if token == "" {
		return true
	}
	_, err := s.service.Authenticate(r.Context(), token)
	if err == nil {
		fail(w, 409, "registration_requires_logout")
		return false
	}
	if !errors.Is(err, service.ErrUnauthorized) {
		failService(w, err)
		return false
	}
	return true
}
func (s *Server) startRegistration(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(w, r) || !s.registrationLoggedOut(w, r) {
		return
	}
	var in struct {
		Name           string `json:"name"`
		Email          string `json:"email"`
		Password       string `json:"password"`
		TurnstileToken string `json:"turnstileToken"`
	}
	if !decode(w, r, &in) {
		return
	}
	c, capability, err := s.service.StartRegistration(r.Context(), in.Email, in.Name, in.Password, in.TurnstileToken, client(r))
	if err != nil {
		failRegistrationCode(w, err, true)
		return
	}
	s.setRegistrationCookie(w, capability, c.ExpiresAt)
	writeJSON(w, http.StatusAccepted, map[string]any{"challenge": c})
}
func (s *Server) getRegistration(w http.ResponseWriter, r *http.Request) {
	c, err := s.service.Registration(r.Context(), s.registrationCookie(r))
	if errors.Is(err, service.ErrInvalid) {
		writeJSON(w, 200, map[string]any{"challenge": nil})
		return
	}
	if err != nil {
		failService(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"challenge": c})
}
func (s *Server) resendRegistration(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(w, r) || !s.registrationLoggedOut(w, r) {
		return
	}
	var in struct {
		TurnstileToken string `json:"turnstileToken"`
	}
	if !decode(w, r, &in) {
		return
	}
	c, err := s.service.ResendRegistration(r.Context(), r.PathValue("id"), s.registrationCookie(r), in.TurnstileToken, client(r))
	if err != nil {
		failRegistrationCode(w, err, false)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"challenge": c})
}
func (s *Server) confirmRegistration(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(w, r) || !s.registrationLoggedOut(w, r) {
		return
	}
	var in struct {
		Code           string `json:"code"`
		TurnstileToken string `json:"turnstileToken"`
	}
	if !decode(w, r, &in) {
		return
	}
	result, err := s.service.ConfirmRegistration(r.Context(), r.PathValue("id"), s.registrationCookie(r), in.Code, in.TurnstileToken, client(r), s.sessionToken(r))
	if err != nil {
		failRegistrationCode(w, err, false)
		return
	}
	s.cookie(w, result.Token)
	s.setRegistrationCookie(w, "", time.Time{})
	writeJSON(w, 200, map[string]any{"account": result.Account, "csrfToken": result.CSRF})
}

func failRegistrationCode(w http.ResponseWriter, err error, start bool) {
	if errors.Is(err, service.ErrRateLimited) {
		err = authcontracts.ErrEmailCodeRateLimited
	}
	if !start && errors.Is(err, service.ErrInvalid) {
		err = authcontracts.ErrEmailCodeInvalid
	}
	failService(w, err)
}
