package authhttp

import (
	"errors"
	"net/http"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/service"
)

func (s *Server) registerNative(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/native/oauth/{provider}/start", s.nativeOAuthStart)
	mux.HandleFunc("POST /api/v1/auth/native/oauth/exchange", s.nativeOAuthExchange)
	mux.HandleFunc("GET /api/v1/auth/native/oauth/registration", s.nativeOAuthRegistration)
	mux.HandleFunc("POST /api/v1/auth/native/oauth/registration/confirm", s.nativeOAuthConfirm)
	mux.HandleFunc("DELETE /api/v1/auth/native/oauth/{provider}", s.nativeOAuthUnlink)
	mux.HandleFunc("POST /api/v1/auth/native/login", s.nativeLogin)
	mux.HandleFunc("POST /api/v1/auth/native/logout", s.nativeLogout)
	mux.HandleFunc("POST /api/v1/auth/native/revoke", s.nativeRevoke)
	mux.HandleFunc("POST /api/v1/auth/native/registration", s.nativeStartRegistration)
	mux.HandleFunc("GET /api/v1/auth/native/registration", s.nativeRegistration)
	mux.HandleFunc("POST /api/v1/auth/native/registration/{id}/resend", s.nativeResendRegistration)
	mux.HandleFunc("POST /api/v1/auth/native/registration/{id}/confirm", s.nativeConfirmRegistration)
	mux.HandleFunc("POST /api/v1/auth/native/password/reset/request", s.nativeResetRequest)
	mux.HandleFunc("POST /api/v1/auth/native/password/reset/confirm", s.nativeResetConfirm)
	mux.HandleFunc("POST /api/v1/auth/native/password/change", s.nativePasswordChange)
}

func nativePublic(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-CSRF-Token") != "" {
		fail(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

func nativeSession(w http.ResponseWriter, result service.LoginResult) {
	writeJSON(w, http.StatusOK, nativeSessionBody(result))
}

func nativeSessionBody(result service.LoginResult) map[string]any {
	session := map[string]any{"id": result.SessionID, "token": result.Token, "expiresAt": result.ExpiresAt}
	if result.RevokeToken != "" {
		session["revokeToken"] = result.RevokeToken
	}
	return map[string]any{"account": result.Account, "session": session}
}

func (s *Server) nativeLogin(w http.ResponseWriter, r *http.Request) {
	if !nativePublic(w, r) {
		return
	}
	var in struct {
		Email, Password, TurnstileToken string
	}
	if !decode(w, r, &in) {
		return
	}
	result, err := s.service.NativeLogin(r.Context(), in.Email, in.Password, in.TurnstileToken, client(r))
	if err != nil {
		failService(w, err)
		return
	}
	nativeSession(w, result)
}

func (s *Server) nativeStartRegistration(w http.ResponseWriter, r *http.Request) {
	if !nativePublic(w, r) {
		return
	}
	var in struct {
		Name, Email, Password, TurnstileToken string
	}
	if !decode(w, r, &in) {
		return
	}
	challenge, capability, err := s.service.StartRegistration(r.Context(), in.Email, in.Name, in.Password, in.TurnstileToken, client(r))
	if err != nil {
		failRegistrationCode(w, err, true)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"challenge": challenge, "registrationToken": capability})
}

func (s *Server) nativeRegistration(w http.ResponseWriter, r *http.Request) {
	if !nativePublic(w, r) {
		return
	}
	challenge, err := s.service.Registration(r.Context(), r.Header.Get("X-Heartbeat-Registration-Token"))
	if errors.Is(err, service.ErrInvalid) {
		writeJSON(w, http.StatusOK, map[string]any{"challenge": nil})
		return
	}
	if err != nil {
		failService(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"challenge": challenge})
}

func (s *Server) nativeResendRegistration(w http.ResponseWriter, r *http.Request) {
	if !nativePublic(w, r) {
		return
	}
	var in struct {
		RegistrationToken, TurnstileToken string
	}
	if !decode(w, r, &in) {
		return
	}
	challenge, err := s.service.ResendRegistration(r.Context(), r.PathValue("id"), in.RegistrationToken, in.TurnstileToken, client(r))
	if err != nil {
		failRegistrationCode(w, err, false)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"challenge": challenge})
}

func (s *Server) nativeConfirmRegistration(w http.ResponseWriter, r *http.Request) {
	if !nativePublic(w, r) {
		return
	}
	var in struct {
		RegistrationToken, Code, TurnstileToken string
	}
	if !decode(w, r, &in) {
		return
	}
	result, err := s.service.NativeConfirmRegistration(r.Context(), r.PathValue("id"), in.RegistrationToken, in.Code, in.TurnstileToken, client(r))
	if err != nil {
		failRegistrationCode(w, err, false)
		return
	}
	nativeSession(w, result)
}

func (s *Server) nativeResetRequest(w http.ResponseWriter, r *http.Request) {
	if !nativePublic(w, r) {
		return
	}
	var in struct{ Email, TurnstileToken string }
	if !decode(w, r, &in) {
		return
	}
	if err := s.service.NativeResetRequest(r.Context(), in.Email, in.TurnstileToken, client(r)); err != nil {
		failService(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) nativeResetConfirm(w http.ResponseWriter, r *http.Request) {
	if !nativePublic(w, r) {
		return
	}
	var in struct{ Token, NewPassword string }
	if !decode(w, r, &in) {
		return
	}
	if err := s.service.ResetConfirm(r.Context(), in.Token, in.NewPassword, client(r)); err != nil {
		failService(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) nativePasswordChange(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.requireNative(w, r)
	if !ok {
		return
	}
	var in struct{ CurrentPassword, NewPassword string }
	if !decode(w, r, &in) {
		return
	}
	if err := s.service.ChangePassword(r.Context(), p, in.CurrentPassword, in.NewPassword, client(r)); err != nil {
		failService(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) nativeLogout(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.requireNative(w, r)
	if !ok {
		return
	}
	if err := s.service.RevokeSession(r.Context(), p, p.SessionID); err != nil {
		failService(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) nativeRevoke(w http.ResponseWriter, r *http.Request) {
	if !nativePublic(w, r) {
		return
	}
	var in struct{ SessionID, RevokeToken string }
	if !decode(w, r, &in) {
		return
	}
	if err := s.service.RevokeNativeSession(r.Context(), in.SessionID, in.RevokeToken, client(r)); err != nil {
		failService(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) nativeOAuthStart(w http.ResponseWriter, r *http.Request) {
	var in struct{ Mode, State, CodeChallenge, Password string }
	if !decode(w, r, &in) {
		return
	}
	input := service.NativeOAuthStartInput{Provider: r.PathValue("provider"), Mode: in.Mode,
		State: in.State, CodeChallenge: in.CodeChallenge, Password: in.Password, Client: client(r)}
	if in.Mode == "link" {
		p, token, ok := s.requireNative(w, r)
		if !ok {
			return
		}
		input.Principal, input.SessionToken = &p, token
	} else if !nativePublic(w, r) {
		return
	}
	result, err := s.service.NativeOAuthStart(r.Context(), input)
	if err != nil {
		failService(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) nativeOAuthExchange(w http.ResponseWriter, r *http.Request) {
	if !nativePublic(w, r) {
		return
	}
	var in service.NativeOAuthExchangeInput
	if !decode(w, r, &in) {
		return
	}
	in.Client = client(r)
	result, err := s.service.NativeOAuthExchange(r.Context(), in)
	if err != nil {
		failService(w, err)
		return
	}
	if result.Login != nil {
		body := nativeSessionBody(*result.Login)
		body["status"] = result.Status
		writeJSON(w, http.StatusOK, body)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) nativeOAuthRegistration(w http.ResponseWriter, r *http.Request) {
	if !nativePublic(w, r) {
		return
	}
	info, err := s.service.OAuthRegistration(r.Context(), r.Header.Get("X-Heartbeat-Registration-Token"))
	if err != nil {
		failService(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"registration": info})
}

func (s *Server) nativeOAuthConfirm(w http.ResponseWriter, r *http.Request) {
	if !nativePublic(w, r) {
		return
	}
	var in struct{ RegistrationToken, Token string }
	if !decode(w, r, &in) {
		return
	}
	result, err := s.service.NativeOAuthConfirm(r.Context(), in.RegistrationToken, in.Token, client(r))
	if err != nil {
		failService(w, err)
		return
	}
	nativeSession(w, result.Login)
}

func (s *Server) nativeOAuthUnlink(w http.ResponseWriter, r *http.Request) {
	p, raw, ok := s.requireNative(w, r)
	if !ok {
		return
	}
	var in struct{ IdentityID, Password string }
	if !decode(w, r, &in) {
		return
	}
	result, err := s.service.NativeOAuthUnlink(r.Context(), p, raw, r.PathValue("provider"), in.IdentityID, in.Password, client(r))
	if err != nil {
		failService(w, err)
		return
	}
	nativeSession(w, result)
}
