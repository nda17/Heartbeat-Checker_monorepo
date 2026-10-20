package service

import "testing"

func TestOAuthReturnToAllowsOnlyCanonicalPushPairingTarget(t *testing.T) {
	const id = "123e4567-e89b-12d3-a456-426614174000"
	valid := "/app/settings?pushPair=" + id
	if got := oauthReturnTo(valid); got != valid {
		t.Fatalf("valid pairing target = %q, want %q", got, valid)
	}

	for _, input := range []string{
		"/app/settings?pushPair=not-a-uuid",
		"/app/settings?pushPair=" + id + "&next=/app",
		"/app/settings?pushPair=" + id + "&pushPair=" + id,
		"https://heartbeat-checker.ru/app/settings?pushPair=" + id,
		"/app/settings?pushPair=" + id + "#fragment",
	} {
		if got := oauthReturnTo(input); got != "/app" {
			t.Errorf("unsafe pairing target %q returned %q, want /app", input, got)
		}
	}
}

func TestOAuthReturnToPreservesExistingAllowedDestinations(t *testing.T) {
	for _, input := range []string{"/app", "/app/admin", "/app/settings"} {
		if got := oauthReturnTo(input); got != input {
			t.Errorf("existing destination %q returned %q", input, got)
		}
	}
	const incident = "/app?monitor=123e4567-e89b-12d3-a456-426614174000&incident=987e6543-e21b-12d3-a456-426614174000"
	if got := oauthReturnTo(incident); got != incident {
		t.Fatalf("existing incident destination = %q, want %q", got, incident)
	}
}
