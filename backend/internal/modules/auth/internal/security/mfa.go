package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"time"
)

func NewTOTPSecret() ([]byte, string, error) {
	secret := make([]byte, 20)
	if _, err := rand.Read(secret); err != nil {
		return nil, "", err
	}
	return secret, base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret), nil
}

// TOTP accepts at most one adjacent 30-second step and rejects prior counters.
func TOTP(secret []byte, code string, now time.Time, lastCounter int64) (int64, bool) {
	if len(secret) != 20 || len(code) != 6 {
		return 0, false
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	current := now.Unix() / 30
	for counter := current - 1; counter <= current+1; counter++ {
		if counter <= lastCounter || counter < 0 {
			continue
		}
		var input [8]byte
		binary.BigEndian.PutUint64(input[:], uint64(counter))
		mac := hmac.New(sha1.New, secret)
		_, _ = mac.Write(input[:])
		sum := mac.Sum(nil)
		offset := int(sum[len(sum)-1] & 0x0f)
		value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
		want := fmt.Sprintf("%06d", value%1000000)
		if hmac.Equal([]byte(want), []byte(code)) {
			return counter, true
		}
	}
	return 0, false
}

func EncryptMFA(key []byte, accountID string, version int64, secret []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, aead.Seal(nil, nonce, secret, mfaAAD(accountID, version))...), nil
}

func DecryptMFA(key []byte, accountID string, version int64, encrypted []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(encrypted) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("invalid encrypted factor")
	}
	return aead.Open(nil, encrypted[:aead.NonceSize()], encrypted[aead.NonceSize():], mfaAAD(accountID, version))
}

func mfaAAD(accountID string, version int64) []byte {
	return []byte("heartbeat-auth-mfa-v1:" + accountID + ":" + strconv.FormatInt(version, 10))
}
