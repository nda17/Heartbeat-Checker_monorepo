package security

import (
	"bytes"
	"testing"
	"time"
)

func TestTOTPMatchesRFC6238SHA1VectorsAndRejectsReplay(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, tc := range []struct {
		unix int64
		want string
	}{
		{unix: 59, want: "287082"},
		{unix: 1111111109, want: "081804"},
		{unix: 1111111111, want: "050471"},
		{unix: 1234567890, want: "005924"},
		{unix: 2000000000, want: "279037"},
		{unix: 20000000000, want: "353130"},
	} {
		now := time.Unix(tc.unix, 0)
		counter, ok := TOTP(secret, tc.want, now, -1)
		if !ok || counter != tc.unix/30 {
			t.Errorf("TOTP(%d)=(%d,%t), want counter %d and RFC code %s", tc.unix, counter, ok, tc.unix/30, tc.want)
		}
		if _, ok := TOTP(secret, tc.want, now, counter); ok {
			t.Errorf("TOTP accepted already-used counter %d", counter)
		}
	}
}

func TestTOTPRejectsMalformedCodesAndWrongSecretSize(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(59, 0)
	for _, code := range []string{"28708", "2870820", "28x082", " 87082", "999999"} {
		if _, ok := TOTP(secret, code, now, -1); ok {
			t.Errorf("TOTP accepted malformed/wrong code %q", code)
		}
	}
	if _, ok := TOTP(secret[:19], "287082", now, -1); ok {
		t.Fatal("TOTP accepted secret of unsupported size")
	}
}

func TestMFASecretEncryptionIsBoundToAccountAndFactorVersion(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	secret := []byte("a twenty byte secret")
	ciphertext, err := EncryptMFA(key, "account-a", 3, secret)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := DecryptMFA(key, "account-a", 3, ciphertext)
	if err != nil || !bytes.Equal(decrypted, secret) {
		t.Fatalf("round trip decrypted=%q err=%v", decrypted, err)
	}
	if _, err := DecryptMFA(key, "account-b", 3, ciphertext); err == nil {
		t.Fatal("MFA ciphertext decrypted under a different account")
	}
	if _, err := DecryptMFA(key, "account-a", 4, ciphertext); err == nil {
		t.Fatal("MFA ciphertext decrypted under a different factor version")
	}
	ciphertext[len(ciphertext)-1] ^= 1
	if _, err := DecryptMFA(key, "account-a", 3, ciphertext); err == nil {
		t.Fatal("tampered MFA ciphertext was accepted")
	}
}
