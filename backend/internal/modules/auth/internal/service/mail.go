package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/repository/postgres"
)

type mailPayload struct {
	Recipient string `json:"recipient"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
}

func DecodeMailKey(encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New("AUTH_MAIL_ENCRYPTION_KEY must be base64 of 32 bytes")
	}
	return key, nil
}

func encryptMail(key []byte, id, purpose string, p mailPayload) ([]byte, error) {
	plain, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plain, []byte(id+":"+purpose)), nil
}

func decryptMail(key []byte, job postgres.MailJob) (mailPayload, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return mailPayload{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return mailPayload{}, err
	}
	if len(job.Payload) < aead.NonceSize() {
		return mailPayload{}, errors.New("invalid encrypted mail payload")
	}
	plain, err := aead.Open(nil, job.Payload[:aead.NonceSize()], job.Payload[aead.NonceSize():], []byte(job.ID+":"+job.Purpose))
	if err != nil {
		return mailPayload{}, errors.New("invalid encrypted mail payload")
	}
	var p mailPayload
	if err = json.Unmarshal(plain, &p); err != nil {
		return mailPayload{}, errors.New("invalid encrypted mail payload")
	}
	return p, nil
}

type JobSender interface {
	SendWithID(context.Context, string, string, string, string) error
}

type MailWorker struct {
	repo   *postgres.Repository
	key    []byte
	sender JobSender
}

func NewMailWorker(repo *postgres.Repository, encodedKey string, sender JobSender) (*MailWorker, error) {
	if repo == nil || sender == nil {
		return nil, errors.New("mail worker dependencies are required")
	}
	key, err := DecodeMailKey(encodedKey)
	if err != nil {
		return nil, err
	}
	return &MailWorker{repo: repo, key: key, sender: sender}, nil
}

// ProcessOne makes at most one SMTP attempt. The lease guards a late result from
// overwriting a newer claim after a process restart.
func (w *MailWorker) ProcessOne(ctx context.Context) (bool, error) {
	job, err := w.repo.ClaimMail(ctx)
	if err != nil || job == nil {
		return false, err
	}
	p, err := decryptMail(w.key, *job)
	if err != nil {
		if completeErr := w.repo.CompleteMail(ctx, *job, false, true); completeErr != nil {
			return true, completeErr
		}
		return true, fmt.Errorf("decrypt auth mail job: %w", err)
	}
	messageID := fmt.Sprintf("<%s@%s>", job.ID, "heartbeat-checker.ru")
	sendCtx, cancel := context.WithDeadline(ctx, job.ExpiresAt)
	err = w.sender.SendWithID(sendCtx, p.Recipient, p.Subject, p.Body, messageID)
	cancel()
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	permanent := false
	var classified interface{ Permanent() bool }
	if errors.As(err, &classified) {
		permanent = classified.Permanent()
	}
	if completeErr := w.repo.CompleteMail(ctx, *job, err == nil, permanent); completeErr != nil {
		return true, completeErr
	}
	return true, nil
}
