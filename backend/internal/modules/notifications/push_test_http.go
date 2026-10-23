package notifications

import (
	"net/http"

	authcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/contracts"
)

func (p *Push) createNativePushTest(w http.ResponseWriter, r *http.Request) {
	actor, ok := authcontracts.FromContext(r.Context())
	if !ok || actor.ClientKind != "android" {
		pushFailure(w, ErrPushUnauthorized)
		return
	}
	var in PushTestInput
	if !pushDecode(w, r, &in) {
		return
	}
	result, created, err := p.CreatePushTest(r.Context(), actor, r.Header.Get("X-Heartbeat-Installation-Credential"), in)
	if err != nil {
		pushFailure(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	messengerWrite(w, status, map[string]any{"test": result})
}

func (p *Push) getNativePushTest(w http.ResponseWriter, r *http.Request) {
	actor, ok := authcontracts.FromContext(r.Context())
	if !ok || actor.ClientKind != "android" {
		pushFailure(w, ErrPushUnauthorized)
		return
	}
	result, err := p.PushTest(r.Context(), actor, r.Header.Get("X-Heartbeat-Installation-Credential"), r.PathValue("id"))
	if err != nil {
		pushFailure(w, err)
		return
	}
	messengerWrite(w, 200, map[string]any{"test": result})
}

func (p *Push) getInstallationPushTest(w http.ResponseWriter, r *http.Request) {
	credential, ok := p.nativeCredential(w, r)
	if !ok {
		return
	}
	result, err := p.PushTestContent(r.Context(), credential, r.PathValue("id"))
	if err != nil {
		pushFailure(w, err)
		return
	}
	messengerWrite(w, 200, result)
}

func (p *Push) receiptInstallationPushTest(w http.ResponseWriter, r *http.Request) {
	credential, ok := p.nativeCredential(w, r)
	if !ok {
		return
	}
	var in PushTestReceipt
	if !pushDecode(w, r, &in) {
		return
	}
	if err := p.RecordPushTestReceipt(r.Context(), credential, r.PathValue("id"), in); err != nil {
		pushFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
