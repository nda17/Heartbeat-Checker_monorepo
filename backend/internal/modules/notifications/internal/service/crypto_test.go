package service

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

const testEncryptionKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func TestNewCipherBoxRequiresStrictBase64Of32Bytes(t *testing.T) {
	for _, encoded := range []string{"", "not-base64", base64.StdEncoding.EncodeToString([]byte("short")), testEncryptionKey + " "} {
		if _, err := newCipherBox(encoded); err == nil {
			t.Errorf("newCipherBox(%q) succeeded, want invalid key", encoded)
		}
	}
	if _, err := newCipherBox(testEncryptionKey); err != nil {
		t.Fatalf("newCipherBox(valid key): %v", err)
	}
}

func TestCipherBoxUsesRandomNonceAndBindsCiphertextToRowAndField(t *testing.T) {
	box, err := newCipherBox(testEncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	const plaintext = "https://hooks.example.test/notify?tenant=secret"
	first, err := box.seal("account-1", "endpoint-1", "url", plaintext)
	if err != nil {
		t.Fatal(err)
	}
	second, err := box.seal("account-1", "endpoint-1", "url", plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("sealing identical values reused the same nonce and ciphertext")
	}
	if bytes.Contains(first, []byte(plaintext)) {
		t.Fatal("ciphertext contains the plaintext URL")
	}
	if got, err := box.open("account-1", "endpoint-1", "url", first); err != nil || got != plaintext {
		t.Fatalf("open() = (%q, %v), want original URL", got, err)
	}
	for _, coordinates := range [][3]string{
		{"account-2", "endpoint-1", "url"},
		{"account-1", "endpoint-2", "url"},
		{"account-1", "endpoint-1", "secret"},
	} {
		if _, err := box.open(coordinates[0], coordinates[1], coordinates[2], first); err == nil {
			t.Errorf("open() with AAD %v succeeded, want authentication failure", coordinates)
		}
	}
	if _, err := box.open("account-1", "endpoint-1", "url", first[:5]); err == nil {
		t.Fatal("open() accepted truncated ciphertext")
	}
}

func TestNewSecretIsUnpaddedURLSafe32ByteSecret(t *testing.T) {
	secret, err := newSecret()
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(secret, "+/=") {
		t.Fatalf("secret %q is not unpadded URL-safe base64", secret)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("secret decodes to %d bytes, error %v; want 32 bytes", len(decoded), err)
	}
}
