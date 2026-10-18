package worker

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSignWebhookAttemptUsesExactBodyBytes(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	body := []byte("{ \"type\" : \"monitor.down\", \"sequence\": 4 }\n")
	now := time.Date(2026, 10, 18, 11, 22, 33, 0, time.UTC)
	timestamp, signature, err := signWebhookAttempt(base64.RawURLEncoding.EncodeToString(key), "7dd7cc82-717d-48b5-b160-ff57c23a97d8", body, now)
	if err != nil {
		t.Fatalf("signWebhookAttempt: %v", err)
	}
	if timestamp != strconv.FormatInt(now.Unix(), 10) {
		t.Fatalf("timestamp = %q, want UTC Unix seconds %d", timestamp, now.Unix())
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(timestamp + ".7dd7cc82-717d-48b5-b160-ff57c23a97d8."))
	_, _ = mac.Write(body)
	want := "v1=" + hex.EncodeToString(mac.Sum(nil))
	if signature != want {
		t.Fatalf("signature = %q, want HMAC over original body %q", signature, want)
	}
	normalized := []byte(`{"type":"monitor.down","sequence":4}`)
	if hmac.Equal([]byte(signature), []byte("v1="+hex.EncodeToString(func() []byte {
		other := hmac.New(sha256.New, key)
		_, _ = other.Write([]byte(timestamp + ".7dd7cc82-717d-48b5-b160-ff57c23a97d8."))
		_, _ = other.Write(normalized)
		return other.Sum(nil)
	}()))) {
		t.Fatal("signature matches normalized JSON instead of the exact stored body")
	}
}

func TestSignWebhookAttemptRejectsInvalidSecret(t *testing.T) {
	for _, encoded := range []string{"", "short", base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 31)))} {
		if _, _, err := signWebhookAttempt(encoded, "event", []byte(`{}`), time.Now()); err == nil {
			t.Errorf("signWebhookAttempt(%q) succeeded, want invalid secret", encoded)
		}
	}
}
