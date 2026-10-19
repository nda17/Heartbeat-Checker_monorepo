package authhttp

import (
	"context"
	"net/http"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/service"
)

func (s *Server) mfaStatus(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.require(w, r, false)
	if !ok {
		return
	}
	status, err := s.service.MFAStatus(r.Context(), p)
	if err != nil {
		failService(w, err)
		return
	}
	writeJSON(w, 200, status)
}

func (s *Server) mfaEnrollment(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.require(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	result, err := s.service.StartMFAEnrollment(r.Context(), p, in.Password, client(r))
	if err != nil {
		failService(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) mfaEnrollmentConfirm(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.require(w, r, true)
	if !ok {
		return
	}
	var in struct {
		EnrollmentID string `json:"enrollmentId"`
		Code         string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	result, err := s.service.ConfirmMFAEnrollment(r.Context(), p, in.EnrollmentID, in.Code, client(r))
	if err != nil {
		failService(w, err)
		return
	}
	s.cookie(w, result.Token)
	writeJSON(w, 200, result)
}

func (s *Server) mfaVerify(w http.ResponseWriter, r *http.Request) {
	s.mfaPasswordCode(w, r, s.service.VerifyMFA)
}

func (s *Server) mfaRecovery(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.require(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Password     string `json:"password"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if !decode(w, r, &in) {
		return
	}
	result, err := s.service.RecoverMFA(r.Context(), p, in.Password, in.RecoveryCode, client(r))
	if err != nil {
		failService(w, err)
		return
	}
	s.cookie(w, result.Token)
	writeJSON(w, 200, result)
}

func (s *Server) mfaPasswordCode(w http.ResponseWriter, r *http.Request, verify func(context.Context, service.Principal, string, string, service.Client) (service.MFAVerification, error)) {
	p, _, ok := s.require(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	result, err := verify(r.Context(), p, in.Password, in.Code, client(r))
	if err != nil {
		failService(w, err)
		return
	}
	s.cookie(w, result.Token)
	writeJSON(w, 200, result)
}

func (s *Server) mfaRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.require(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	codes, err := s.service.ReplaceMFARecoveryCodes(r.Context(), p, in.Password, in.Code, client(r))
	if err != nil {
		failService(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"recoveryCodes": codes})
}
