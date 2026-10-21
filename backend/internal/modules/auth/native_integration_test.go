package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
	authservice "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/service"
)

func nativeProof(t *testing.T) string {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func nativeRequest(t *testing.T, f *authFixture, method, path string, payload any, bearer string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if payload == nil {
		body = strings.NewReader("")
	} else {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = strings.NewReader(string(data))
	}
	req := httptest.NewRequest(method, path, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	req.RemoteAddr = netJoin("192.0.2.10", "43125")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func TestNativeSessionIsBearerOnlyAndRevocableWithoutChangingWebCookieFlow(t *testing.T) {
	f := newAuthFixture(t)
	const email = "native-session@example.test"
	token := f.register(t, email, "Native User", integrationPass)
	if got := f.verify(t, token, integrationPass).Code; got != http.StatusNoContent {
		t.Fatalf("verify status=%d, want 204", got)
	}
	login := nativeRequest(t, f, http.MethodPost, "/api/v1/auth/native/login", map[string]string{
		"email": email, "password": integrationPass, "turnstileToken": integrationToken,
	}, "", nil)
	if login.Code != http.StatusOK {
		t.Fatalf("native login status=%d body=%s", login.Code, login.Body.String())
	}
	var session struct {
		Session struct {
			ID          string `json:"id"`
			Token       string `json:"token"`
			RevokeToken string `json:"revokeToken"`
		} `json:"session"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if len(session.Session.Token) != 43 || len(session.Session.RevokeToken) != 43 || session.Session.Token == session.Session.RevokeToken {
		t.Fatalf("native login returned malformed or overlapping credentials")
	}
	var clientKind string
	var tokenHash []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT client_kind,token_hash FROM auth.sessions WHERE id=$1`, session.Session.ID).Scan(&clientKind, &tokenHash); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(session.Session.Token))
	if clientKind != "android" || string(tokenHash) != string(hash[:]) {
		t.Fatalf("stored native session kind=%q or token hash mismatch", clientKind)
	}
	if got := nativeRequest(t, f, http.MethodPost, "/api/v1/auth/native/logout", nil, "", &http.Cookie{Name: "hc_session", Value: session.Session.Token}).Code; got != http.StatusUnauthorized {
		t.Fatalf("cookie-authenticated native logout status=%d, want 401", got)
	}
	if _, err := f.module.service.Authenticate(context.Background(), session.Session.Token); err == nil {
		t.Fatal("native bearer token authenticated as a web cookie session")
	}
	if err := f.module.service.RevokeNativeSession(context.Background(), session.Session.ID, strings.Repeat("x", 43), authservice.Client{IP: "192.0.2.10"}); err != nil {
		t.Fatalf("revoke with wrong credential: %v", err)
	}
	if _, err := f.module.service.AuthenticateNative(context.Background(), session.Session.Token); err != nil {
		t.Fatalf("wrong revoke credential revoked native session: %v", err)
	}
	if err := f.module.service.RevokeNativeSession(context.Background(), session.Session.ID, session.Session.RevokeToken, authservice.Client{IP: "192.0.2.10"}); err != nil {
		t.Fatalf("revoke native session: %v", err)
	}
	if _, err := f.module.service.AuthenticateNative(context.Background(), session.Session.Token); err == nil {
		t.Fatal("valid revocation credential did not revoke native session")
	}
	webCookie, _, webLogin := f.login(t, email, integrationPass)
	if webLogin.Code != http.StatusOK || webCookie == nil {
		t.Fatalf("web login status=%d, want existing cookie flow", webLogin.Code)
	}
	if got := nativeRequest(t, f, http.MethodPost, "/api/v1/auth/native/logout", nil, "", webCookie).Code; got != http.StatusUnauthorized {
		t.Fatalf("web cookie reached native endpoint: status=%d", got)
	}
	if _, err := f.module.service.Authenticate(context.Background(), webCookie.Value); err != nil {
		t.Fatalf("existing web cookie session no longer authenticates: %v", err)
	}
}

func TestNativeOAuthPKCEAttemptIsOneUseAndStateBound(t *testing.T) {
	const email = "native-oauth@example.test"
	f := oauthFixture(t, OAuthIdentityProfile{Subject: "native-oauth-subject", Email: email, Name: "OAuth User"})
	f.signup(t, email)
	var accountID string
	if err := f.pool.QueryRow(context.Background(), `SELECT account_id::text FROM auth.credentials WHERE email=$1`, email).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	identityID, err := security.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO auth.oauth_identities(id,account_id,provider,subject) VALUES($1,$2,'yandex','native-oauth-subject')`, identityID, accountID); err != nil {
		t.Fatal(err)
	}
	state, verifier := nativeProof(t), nativeProof(t)
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	start := nativeRequest(t, f, http.MethodPost, "/api/v1/auth/native/oauth/yandex/start", map[string]string{
		"mode": "login", "state": state, "codeChallenge": challenge,
	}, "", nil)
	if start.Code != http.StatusOK {
		t.Fatalf("native OAuth start status=%d body=%s", start.Code, start.Body.String())
	}
	var started struct {
		FlowID string `json:"flowId"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &started); err != nil || started.FlowID == "" {
		t.Fatalf("decode OAuth flow ID: %v", err)
	}
	wrongVerifier := nativeRequest(t, f, http.MethodPost, "/api/v1/auth/native/oauth/exchange", map[string]string{
		"flowId": started.FlowID, "state": state, "code": "fake-code", "codeVerifier": nativeProof(t), "deviceId": "fake-device",
	}, "", nil)
	if wrongVerifier.Code < 400 {
		t.Fatalf("OAuth exchange accepted wrong PKCE verifier: %d %s", wrongVerifier.Code, wrongVerifier.Body.String())
	}
	good := nativeRequest(t, f, http.MethodPost, "/api/v1/auth/native/oauth/exchange", map[string]string{
		"flowId": started.FlowID, "state": state, "code": "fake-code", "codeVerifier": verifier, "deviceId": "fake-device",
	}, "", nil)
	if good.Code != http.StatusOK {
		t.Fatalf("native OAuth exchange status=%d body=%s", good.Code, good.Body.String())
	}
	var exchanged struct {
		Status  string `json:"status"`
		Session struct {
			Token string `json:"token"`
		} `json:"session"`
	}
	if err := json.Unmarshal(good.Body.Bytes(), &exchanged); err != nil || exchanged.Status != "authenticated" || len(exchanged.Session.Token) != 43 {
		t.Fatalf("OAuth exchange did not issue native session: status=%q tokenLength=%d err=%v", exchanged.Status, len(exchanged.Session.Token), err)
	}
	replay := nativeRequest(t, f, http.MethodPost, "/api/v1/auth/native/oauth/exchange", map[string]string{
		"flowId": started.FlowID, "state": state, "code": "fake-code", "codeVerifier": verifier, "deviceId": "fake-device",
	}, "", nil)
	if replay.Code < 400 {
		t.Fatalf("OAuth flow replay succeeded: %d %s", replay.Code, replay.Body.String())
	}
}

func TestNativeOAuthLinkAndUnlinkRotateAccessButPreserveRevokeCapability(t *testing.T) {
	const email = "native-oauth-rotation@example.test"
	f := oauthFixture(t, OAuthIdentityProfile{Subject: "native-oauth-rotation-subject", Email: "unused@example.test", Name: "OAuth User"})
	registration := f.register(t, email, "Native OAuth User", integrationPass)
	if got := f.verify(t, registration, integrationPass).Code; got != http.StatusNoContent {
		t.Fatalf("verify status=%d want 204", got)
	}
	login := nativeRequest(t, f, http.MethodPost, "/api/v1/auth/native/login", map[string]string{
		"email": email, "password": integrationPass, "turnstileToken": integrationToken,
	}, "", nil)
	if login.Code != http.StatusOK {
		t.Fatalf("native login status=%d body=%s", login.Code, login.Body.String())
	}
	type nativeSessionPayload struct {
		Account struct {
			ID string `json:"id"`
		} `json:"account"`
		Session struct {
			ID          string `json:"id"`
			Token       string `json:"token"`
			RevokeToken string `json:"revokeToken"`
		} `json:"session"`
	}
	var initial nativeSessionPayload
	if err := json.Unmarshal(login.Body.Bytes(), &initial); err != nil || initial.Session.ID == "" || initial.Session.Token == "" || initial.Session.RevokeToken == "" {
		t.Fatalf("decode native login capability: %v", err)
	}
	var originalRevokeHash []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT revoke_token_hash FROM auth.sessions WHERE id=$1`, initial.Session.ID).Scan(&originalRevokeHash); err != nil {
		t.Fatal(err)
	}
	expectedRevokeHash := security.Digest(initial.Session.RevokeToken)
	if !bytes.Equal(originalRevokeHash, expectedRevokeHash) {
		t.Fatal("login revoke capability hash does not match stored session capability")
	}

	state, verifier := nativeProof(t), nativeProof(t)
	challengeHash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeHash[:])
	start := nativeRequest(t, f, http.MethodPost, "/api/v1/auth/native/oauth/yandex/start", map[string]string{
		"mode": "link", "state": state, "codeChallenge": challenge, "password": integrationPass,
	}, initial.Session.Token, nil)
	if start.Code != http.StatusOK {
		t.Fatalf("link start status=%d body=%s", start.Code, start.Body.String())
	}
	var flow struct {
		FlowID string `json:"flowId"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &flow); err != nil || flow.FlowID == "" {
		t.Fatalf("decode link flow: %v", err)
	}
	linked := nativeRequest(t, f, http.MethodPost, "/api/v1/auth/native/oauth/exchange", map[string]string{
		"flowId": flow.FlowID, "state": state, "code": "fake-code", "codeVerifier": verifier, "deviceId": "fake-device",
	}, "", nil)
	if linked.Code != http.StatusOK {
		t.Fatalf("link exchange status=%d body=%s", linked.Code, linked.Body.String())
	}
	var afterLink nativeSessionPayload
	if err := json.Unmarshal(linked.Body.Bytes(), &afterLink); err != nil || afterLink.Session.ID != initial.Session.ID || afterLink.Session.Token == "" || afterLink.Session.Token == initial.Session.Token {
		t.Fatalf("link did not rotate access token on the same session: %v %+v", err, afterLink.Session)
	}
	var returned map[string]any
	if err := json.Unmarshal(linked.Body.Bytes(), &returned); err != nil {
		t.Fatal(err)
	}
	if _, present := returned["session"].(map[string]any)["revokeToken"]; present {
		t.Fatal("link response re-exposed the stable revoke capability")
	}
	if _, err := f.module.service.AuthenticateNative(context.Background(), initial.Session.Token); err == nil {
		t.Fatal("pre-link access bearer remained valid after rotation")
	}
	var afterUnlink nativeSessionPayload
	assertStableNativeRevokeHash := func(stage string) {
		t.Helper()
		var accountID, clientKind string
		var accessHash, revokeHash []byte
		if err := f.pool.QueryRow(context.Background(), `SELECT account_id::text,client_kind,token_hash,revoke_token_hash FROM auth.sessions WHERE id=$1`, initial.Session.ID).
			Scan(&accountID, &clientKind, &accessHash, &revokeHash); err != nil {
			t.Fatal(err)
		}
		wantAccess := security.Digest(afterLink.Session.Token)
		if stage == "unlink" {
			wantAccess = security.Digest(afterUnlink.Session.Token)
		}
		if accountID != initial.Account.ID || clientKind != "android" || !bytes.Equal(accessHash, wantAccess) || !bytes.Equal(revokeHash, expectedRevokeHash) {
			t.Fatalf("%s changed SID/account/kind or revoke capability: account=%s client=%s accessMatch=%t revokeMatch=%t", stage, accountID, clientKind, bytes.Equal(accessHash, wantAccess), bytes.Equal(revokeHash, expectedRevokeHash))
		}
	}
	assertStableNativeRevokeHash("link")
	var identityID string
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM auth.oauth_identities WHERE account_id=$1 AND provider='yandex' AND subject='native-oauth-rotation-subject'`, initial.Account.ID).Scan(&identityID); err != nil {
		t.Fatal(err)
	}
	unlinked := nativeRequest(t, f, http.MethodDelete, "/api/v1/auth/native/oauth/yandex", map[string]string{
		"identityId": identityID, "password": integrationPass,
	}, afterLink.Session.Token, nil)
	if unlinked.Code != http.StatusOK {
		t.Fatalf("native unlink status=%d body=%s", unlinked.Code, unlinked.Body.String())
	}
	if err := json.Unmarshal(unlinked.Body.Bytes(), &afterUnlink); err != nil || afterUnlink.Session.ID != initial.Session.ID || afterUnlink.Session.Token == "" || afterUnlink.Session.Token == afterLink.Session.Token {
		t.Fatalf("unlink did not rotate access token on the same session: %v %+v", err, afterUnlink.Session)
	}
	var unlinkBody map[string]any
	if err := json.Unmarshal(unlinked.Body.Bytes(), &unlinkBody); err != nil {
		t.Fatal(err)
	}
	if _, present := unlinkBody["session"].(map[string]any)["revokeToken"]; present {
		t.Fatal("unlink response re-exposed the stable revoke capability")
	}
	assertStableNativeRevokeHash("unlink")
	if _, err := f.module.service.AuthenticateNative(context.Background(), afterLink.Session.Token); err == nil {
		t.Fatal("pre-unlink access bearer remained valid after rotation")
	}
	if got := nativeRequest(t, f, http.MethodPost, "/api/v1/auth/native/revoke", map[string]string{
		"sessionId": initial.Session.ID, "revokeToken": initial.Session.RevokeToken,
	}, "", nil); got.Code != http.StatusNoContent {
		t.Fatalf("original revoke capability failed after two access rotations: %d %s", got.Code, got.Body.String())
	}
	if _, err := f.module.service.AuthenticateNative(context.Background(), afterUnlink.Session.Token); err == nil {
		t.Fatal("stable old revoke capability did not revoke the latest rotated access bearer")
	}
}
