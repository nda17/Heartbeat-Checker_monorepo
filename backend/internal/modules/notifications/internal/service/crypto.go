package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

type cipherBox struct{ key []byte }

func newCipherBox(encoded string) (cipherBox, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return cipherBox{}, errors.New("WEBHOOK_ENCRYPTION_KEY must be base64 of 32 bytes")
	}
	return cipherBox{key}, nil
}

func (c cipherBox) seal(accountID, endpointID, field, value string) ([]byte, error) {
	block, err := aes.NewCipher(c.key)
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
	return aead.Seal(nonce, nonce, []byte(value), []byte(fmt.Sprintf("%s:%s:%s", accountID, endpointID, field))), nil
}

func (c cipherBox) open(accountID, endpointID, field string, data []byte) (string, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < aead.NonceSize() {
		return "", errors.New("invalid encrypted webhook field")
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(fmt.Sprintf("%s:%s:%s", accountID, endpointID, field)))
	if err != nil {
		return "", errors.New("invalid encrypted webhook field")
	}
	return string(plain), nil
}

func newSecret() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(secret), nil
}
