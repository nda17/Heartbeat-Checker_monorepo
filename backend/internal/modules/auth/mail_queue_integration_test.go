package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/repository/postgres"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/service"
)

const queueTestKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

type queueTestSender struct {
	errors     []error
	ids        []string
	recipients []string
	deadlines  []time.Time
}

func (s *queueTestSender) SendWithID(ctx context.Context, to, _, _, id string) error {
	s.recipients = append(s.recipients, to)
	s.ids = append(s.ids, id)
	if deadline, ok := ctx.Deadline(); ok {
		s.deadlines = append(s.deadlines, deadline)
	} else {
		s.deadlines = append(s.deadlines, time.Time{})
	}
	if len(s.errors) == 0 {
		return nil
	}
	err := s.errors[0]
	s.errors = s.errors[1:]
	return err
}

type permanentQueueError struct{}

func (permanentQueueError) Error() string   { return "permanent test failure" }
func (permanentQueueError) Permanent() bool { return true }

func newMailQueueFixture(t *testing.T) *authFixture {
	t.Helper()
	f := newAuthFixtureWithPoolSizeAndPrefix(t, 1, "heartbeat_authmail_test_")
	module, err := New(f.pool, accounts.New(f.pool), integrationCaptcha{}, nil, Config{
		Origin: integrationOrigin, MailEncryptionKey: queueTestKey,
		ChallengeTTL: time.Hour, ResetTTL: time.Hour, RateLimitWindow: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.module, f.h = module, module.Handler()
	return f
}

func queueRegistration(t *testing.T, f *authFixture, email string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"email":"`+email+`","name":"Queued User","password":"secure test password","turnstileToken":"`+integrationToken+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", integrationOrigin)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("queued registration status=%d body=%s", w.Code, w.Body.String())
	}
	var id string
	if err := f.pool.QueryRow(context.Background(), "SELECT id FROM auth.mail_jobs WHERE purpose='registration' ORDER BY created_at DESC LIMIT 1").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func queueWorker(t *testing.T, f *authFixture, sender service.JobSender) *MailWorker {
	t.Helper()
	worker, err := NewMailWorker(f.pool, queueTestKey, sender)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func TestMailQueueStoresEncryptedPayloadAndRetriesWithStableMessageID(t *testing.T) {
	f := newMailQueueFixture(t)
	const email = "queued-secret@example.test"
	jobID := queueRegistration(t, f, email)
	var payload []byte
	if err := f.pool.QueryRow(context.Background(), "SELECT payload FROM auth.mail_jobs WHERE id=$1", jobID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if bytes := string(payload); strings.Contains(bytes, email) || strings.Contains(bytes, "Confirm your Heartbeat Checker email") || strings.Contains(bytes, "verify-email") {
		t.Fatalf("mail queue stored cleartext payload: %q", bytes)
	}
	if len(payload) < 32 {
		t.Fatalf("encrypted mail payload is unexpectedly short: %d", len(payload))
	}

	sender := &queueTestSender{errors: []error{errors.New("temporary SMTP failure"), nil}}
	worker := queueWorker(t, f, sender)
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("first ProcessOne() = %t, %v", processed, err)
	}
	var status string
	var attempts int
	if err := f.pool.QueryRow(context.Background(), "SELECT status,attempts FROM auth.mail_jobs WHERE id=$1", jobID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 1 {
		t.Fatalf("after retry status=%q attempts=%d", status, attempts)
	}
	if _, err := f.pool.Exec(context.Background(), "UPDATE auth.mail_jobs SET next_attempt_at=now() WHERE id=$1", jobID); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("second ProcessOne() = %t, %v", processed, err)
	}
	if len(sender.ids) != 2 || sender.ids[0] == "" || sender.ids[0] != sender.ids[1] {
		t.Fatalf("attempt Message-IDs = %v, want same stable ID", sender.ids)
	}
	if sender.recipients[0] != email || sender.recipients[1] != email {
		t.Fatalf("recipients = %v", sender.recipients)
	}
	if err := f.pool.QueryRow(context.Background(), "SELECT status,attempts,payload FROM auth.mail_jobs WHERE id=$1", jobID).Scan(&status, &attempts, &payload); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || attempts != 2 || len(payload) != 0 {
		t.Fatalf("after successful retry status=%q attempts=%d payload length=%d", status, attempts, len(payload))
	}
}

func TestMailQueueTerminalFailureAndExpiredLeaseRecovery(t *testing.T) {
	f := newMailQueueFixture(t)
	jobID := queueRegistration(t, f, "terminal@example.test")
	sender := &queueTestSender{errors: []error{permanentQueueError{}}}
	worker := queueWorker(t, f, sender)
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("ProcessOne() = %t, %v", processed, err)
	}
	var status string
	var payload []byte
	if err := f.pool.QueryRow(context.Background(), "SELECT status,payload FROM auth.mail_jobs WHERE id=$1", jobID).Scan(&status, &payload); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || len(payload) != 0 {
		t.Fatalf("terminal result status=%q payload length=%d", status, len(payload))
	}

	retryID := queueRegistration(t, f, "lease@example.test")
	repo := postgres.New(f.pool)
	first, err := repo.ClaimMail(context.Background())
	if err != nil || first == nil {
		t.Fatalf("first ClaimMail() = %#v, %v", first, err)
	}
	// The terminal job is excluded, so this claim must be the second registration.
	if first.ID != retryID {
		t.Fatalf("ClaimMail() claimed %q, want %q", first.ID, retryID)
	}
	if second, err := repo.ClaimMail(context.Background()); err != nil || second != nil {
		t.Fatalf("second claim while leased = %#v, %v", second, err)
	}
	if _, err := f.pool.Exec(context.Background(), "UPDATE auth.mail_jobs SET lease_until=now()-interval '1 second' WHERE id=$1", retryID); err != nil {
		t.Fatal(err)
	}
	second, err := repo.ClaimMail(context.Background())
	if err != nil || second == nil {
		t.Fatalf("claim after lease expiry = %#v, %v", second, err)
	}
	if first.LeaseToken == second.LeaseToken {
		t.Fatal("reclaimed job retained stale lease token")
	}
	if err := repo.CompleteMail(context.Background(), *first, true, false); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var currentLease string
	if err := f.pool.QueryRow(context.Background(), "SELECT status,attempts,lease_token,payload FROM auth.mail_jobs WHERE id=$1", retryID).Scan(&status, &attempts, &currentLease, &payload); err != nil {
		t.Fatal(err)
	}
	if status != "sending" || attempts != 2 || currentLease != second.LeaseToken || len(payload) == 0 {
		t.Fatalf("stale completion changed active lease: status=%q attempts=%d lease=%q payload length=%d", status, attempts, currentLease, len(payload))
	}
	if err := repo.CompleteMail(context.Background(), *second, true, false); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), "SELECT status,attempts FROM auth.mail_jobs WHERE id=$1", retryID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || attempts != 2 {
		t.Fatalf("current lease completion status=%q attempts=%d", status, attempts)
	}

	exhaustedID := queueRegistration(t, f, "attempt-limit@example.test")
	if _, err := f.pool.Exec(context.Background(), `UPDATE auth.mail_jobs SET status='sending',attempts=8,
		lease_token='30000000-0000-4000-8000-000000000001',lease_until=now()-interval '1 second'
		WHERE id=$1`, exhaustedID); err != nil {
		t.Fatal(err)
	}
	if job, err := repo.ClaimMail(context.Background()); err != nil || job != nil {
		t.Fatalf("ClaimMail() for exhausted expired lease = %#v, %v", job, err)
	}
	var leaseToken string
	if err := f.pool.QueryRow(context.Background(), "SELECT status,attempts,payload,COALESCE(lease_token::text,'') FROM auth.mail_jobs WHERE id=$1", exhaustedID).Scan(&status, &attempts, &payload, &leaseToken); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attempts != 8 || len(payload) != 0 || leaseToken != "" {
		t.Fatalf("exhausted expired lease status=%q attempts=%d payload length=%d lease=%v", status, attempts, len(payload), leaseToken)
	}
}

func TestMailWorkerCapsSenderContextAtTokenExpiryAndParentDeadline(t *testing.T) {
	f := newMailQueueFixture(t)
	firstID := queueRegistration(t, f, "deadline-token@example.test")
	var expiresAt time.Time
	if err := f.pool.QueryRow(context.Background(), "SELECT expires_at FROM auth.mail_jobs WHERE id=$1", firstID).Scan(&expiresAt); err != nil {
		t.Fatal(err)
	}
	sender := &queueTestSender{}
	worker := queueWorker(t, f, sender)
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("ProcessOne() = %t, %v", processed, err)
	}
	if len(sender.deadlines) != 1 || !sender.deadlines[0].Equal(expiresAt) {
		t.Fatalf("sender deadline=%v, want token expiry %v", sender.deadlines, expiresAt)
	}

	secondID := queueRegistration(t, f, "deadline-parent@example.test")
	var secondExpiry time.Time
	if err := f.pool.QueryRow(context.Background(), "SELECT expires_at FROM auth.mail_jobs WHERE id=$1", secondID).Scan(&secondExpiry); err != nil {
		t.Fatal(err)
	}
	parentDeadline := secondExpiry.Add(-time.Minute)
	parentCtx, cancel := context.WithDeadline(context.Background(), parentDeadline)
	defer cancel()
	if processed, err := worker.ProcessOne(parentCtx); err != nil || !processed {
		t.Fatalf("ProcessOne() with parent deadline = %t, %v", processed, err)
	}
	if len(sender.deadlines) != 2 || !sender.deadlines[1].Equal(parentDeadline) || !sender.deadlines[1].Before(secondExpiry) {
		t.Fatalf("sender deadline=%v, want earlier parent deadline %v before token expiry %v", sender.deadlines, parentDeadline, secondExpiry)
	}
}

func TestMailQueueDoesNotClaimConsumedOrExpiredTokensAndCleanupClearsPayload(t *testing.T) {
	f := newMailQueueFixture(t)
	repo := postgres.New(f.pool)
	consumedID := queueRegistration(t, f, "consumed@example.test")
	var tokenHash []byte
	if err := f.pool.QueryRow(context.Background(), "SELECT token_hash FROM auth.mail_jobs WHERE id=$1", consumedID).Scan(&tokenHash); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), "UPDATE auth.registration_challenges SET consumed_at=now() WHERE token_hash=$1", tokenHash); err != nil {
		t.Fatal(err)
	}
	var txErr error
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	txErr = postgres.Cleanup(context.Background(), tx)
	if txErr != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(txErr)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status string
	var payload []byte
	if err := f.pool.QueryRow(context.Background(), "SELECT status,payload FROM auth.mail_jobs WHERE id=$1", consumedID).Scan(&status, &payload); err != nil {
		t.Fatal(err)
	}
	if status != "expired" || len(payload) != 0 {
		t.Fatalf("consumed-token cleanup status=%q payload length=%d", status, len(payload))
	}
	if job, err := repo.ClaimMail(context.Background()); err != nil || job != nil {
		t.Fatalf("ClaimMail() for consumed token = %#v, %v", job, err)
	}

	expiredID := queueRegistration(t, f, "expired@example.test")
	if _, err := f.pool.Exec(context.Background(), "UPDATE auth.mail_jobs SET expires_at=now()-interval '1 second' WHERE id=$1", expiredID); err != nil {
		t.Fatal(err)
	}
	tx, err = f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.Cleanup(context.Background(), tx); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), "SELECT status,payload FROM auth.mail_jobs WHERE id=$1", expiredID).Scan(&status, &payload); err != nil {
		t.Fatal(err)
	}
	if status != "expired" || len(payload) != 0 {
		t.Fatalf("expired-token cleanup status=%q payload length=%d", status, len(payload))
	}
}

func TestMailQueueInsertIsAtomicWhenJobInsertFails(t *testing.T) {
	f := newAuthFixtureWithPoolSizeAndPrefix(t, 1, "heartbeat_authmail_test_")
	repo := postgres.New(f.pool)
	ctx := context.Background()
	jobID := "10000000-0000-4000-8000-000000000001"
	challenge := postgres.Challenge{ID: "20000000-0000-4000-8000-000000000001", Email: "atomic-one@example.test", Name: "Atomic", PasswordHash: "hash", ExpiresAt: time.Now().Add(time.Hour)}
	if err := repo.InsertChallengeWithMail(ctx, challenge, security.Digest("token-one"), []byte("encrypted"), jobID); err != nil {
		t.Fatal(err)
	}
	challenge.ID = "20000000-0000-4000-8000-000000000002"
	challenge.Email = "atomic-two@example.test"
	if err := repo.InsertChallengeWithMail(ctx, challenge, security.Digest("token-two"), []byte("encrypted"), jobID); err == nil {
		t.Fatal("InsertChallengeWithMail() error = nil on duplicate job ID")
	}
	var challengeExists, jobExists bool
	if err := f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM auth.registration_challenges WHERE email=$1), EXISTS(SELECT 1 FROM auth.mail_jobs WHERE token_hash=$2)", challenge.Email, security.Digest("token-two")).Scan(&challengeExists, &jobExists); err != nil {
		t.Fatal(err)
	}
	if challengeExists || jobExists {
		t.Fatalf("failed queue insert was not atomic: challenge=%t job=%t", challengeExists, jobExists)
	}
}
