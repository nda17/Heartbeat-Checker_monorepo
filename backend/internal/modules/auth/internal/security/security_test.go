package security

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestRandomTokenHasOpaqueURLSafeShape(t *testing.T) {
	first, err := Token()
	if err != nil {
		t.Fatal(err)
	}
	second, err := Token()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 43 || strings.ContainsAny(first, "+/=") {
		t.Fatalf("random token has unexpected shape %q", first)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(first)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("random token does not encode 32 bytes: len=%d err=%v", len(decoded), err)
	}
	if first == second {
		t.Fatal("two random tokens unexpectedly match")
	}
}

func TestRandomIDIsValidAndUnique(t *testing.T) {
	one, err := ID()
	if err != nil {
		t.Fatal(err)
	}
	two, err := ID()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidID(one) || !ValidID(two) || one == two {
		t.Fatalf("IDs are invalid or unexpectedly equal: %q, %q", one, two)
	}
}

func TestCSRFTokenIsStableAndBoundToSessionSecret(t *testing.T) {
	one := CSRF("session-secret-one")
	if one != CSRF("session-secret-one") {
		t.Fatal("same session secret produced different CSRF tokens")
	}
	if one == CSRF("session-secret-two") {
		t.Fatal("different session secrets produced same CSRF token")
	}
	if len(one) != 43 {
		t.Fatalf("CSRF token length = %d, want 43", len(one))
	}
}

func TestEmailNormalizationAndValidation(t *testing.T) {
	if got := NormalizeEmail("  User.Name+tag@Example.COM \n"); got != "user.name+tag@example.com" {
		t.Fatalf("normalizeEmail() = %q", got)
	}
	for _, address := range []string{"user@example.com", "a+b@sub.example.org"} {
		if !ValidEmail(address) {
			t.Errorf("validEmail(%q) = false", address)
		}
	}
	for _, address := range []string{"", "a@b", "@example.com", "user @example.com", "user@example.com\r\nBcc:x@y.z"} {
		if ValidEmail(address) {
			t.Errorf("validEmail(%q) = true, want false", address)
		}
	}
}

func TestHashPasswordAndCheckPassword(t *testing.T) {
	const password = "correct horse battery staple"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, password) || !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Fatalf("password hash has unexpected encoding: %q", hash)
	}
	if !CheckPassword(hash, password) {
		t.Fatal("checkPassword() rejected correct password")
	}
	if CheckPassword(hash, "wrong password") {
		t.Fatal("checkPassword() accepted wrong password")
	}
	for _, malformed := range []string{"", "argon2id", "$argon2id$v=18$m=19456,t=2,p=1$abc$def", "$argon2id$v=19$m=19456,t=2,p=1$%%%$%%%"} {
		if CheckPassword(malformed, password) {
			t.Errorf("checkPassword() accepted malformed hash %q", malformed)
		}
	}
}

func TestHashPasswordEnforcesLengthBounds(t *testing.T) {
	for _, password := range []string{"short", strings.Repeat("x", 1025)} {
		if _, err := HashPassword(password); err == nil {
			t.Errorf("hashPassword(len=%d) error = nil", len(password))
		}
	}
}

func TestValidIDRejectsMalformedIdentifiers(t *testing.T) {
	for _, id := range []string{"", "short", "00000000-0000-0000-0000-00000000000G", "00000000000000000000000000000000"} {
		if ValidID(id) {
			t.Errorf("ValidID(%q) = true", id)
		}
	}
}
