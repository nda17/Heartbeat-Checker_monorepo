package notifications

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPushChallengeValidationMatchesS256VerifierShape(t *testing.T) {
	verifier := strings.Repeat("v", 43)
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	if !pushChallenge(challenge) {
		t.Fatalf("valid unpadded base64url SHA-256 challenge was rejected: %q", challenge)
	}

	for _, value := range []string{
		challenge + "=",
		challenge[:42],
		strings.Repeat("+", 43),
		base64.RawURLEncoding.EncodeToString(make([]byte, 31)),
	} {
		if pushChallenge(value) {
			t.Errorf("invalid challenge accepted: %q", value)
		}
	}
}

type pushRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f pushRoundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRuStorePushSenderUsesSyntheticHTTPAndOpaquePayload(t *testing.T) {
	var request *http.Request
	sender, err := NewRuStorePushSender("project-test", strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	sender.client = &http.Client{Transport: pushRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		request = r
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
	})}
	if err := sender.Send(context.Background(), "provider-token", map[string]string{"deliveryId": "opaque-id", "bindingGeneration": "3"}, time.Minute); err != nil {
		t.Fatalf("synthetic provider send: %v", err)
	}
	if request == nil || request.URL.Host != "vkpns.rustore.ru" || request.Method != http.MethodPost {
		t.Fatalf("provider request = %+v, want HTTPS provider POST", request)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer "+strings.Repeat("s", 32) {
		t.Fatalf("provider authorization = %q, want configured synthetic bearer", got)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"incident", "monitorName", "body", "title"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("provider notification payload contains clear content field %q: %s", forbidden, body)
		}
	}
	for _, required := range []string{"provider-token", "opaque-id", "bindingGeneration", "3"} {
		if !strings.Contains(string(body), required) {
			t.Errorf("provider payload missing expected routing value %q: %s", required, body)
		}
	}
}

func TestPushAppVersionAndTokenBoundaries(t *testing.T) {
	for _, value := range []string{"1.2.3", "1.2.3+42", "release_candidate-1"} {
		if !pushAppVersion(value) {
			t.Errorf("valid app version rejected: %q", value)
		}
	}
	for _, value := range []string{"", strings.Repeat("a", 33), "1.2.3/4", "1.2.3 4"} {
		if pushAppVersion(value) {
			t.Errorf("invalid app version accepted: %q", value)
		}
	}

	for _, value := range []string{"fcm-token", strings.Repeat("x", 4096)} {
		if !pushToken(value) {
			t.Errorf("valid push token rejected (length %d)", len(value))
		}
	}
	for _, value := range []string{"", strings.Repeat("x", 4097), "token with space", "token\nvalue"} {
		if pushToken(value) {
			t.Errorf("invalid push token accepted (length %d)", len(value))
		}
	}
}

func TestPushSealedTokenIsBoundToInstallationAndField(t *testing.T) {
	block, err := aes.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	p := &Push{}
	p.box, err = cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}

	sealed, err := p.seal("installation-a", "token", []byte("provider-secret"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := p.open("installation-a", "token", sealed)
	if err != nil || string(opened) != "provider-secret" {
		t.Fatalf("open sealed token = (%q, %v), want original token", opened, err)
	}
	for _, tc := range []struct{ id, field string }{
		{id: "installation-b", field: "token"},
		{id: "installation-a", field: "other"},
	} {
		if _, err := p.open(tc.id, tc.field, sealed); err == nil {
			t.Errorf("ciphertext opened with different associated context (%q, %q)", tc.id, tc.field)
		}
	}

	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := p.open("installation-a", "token", tampered); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}
