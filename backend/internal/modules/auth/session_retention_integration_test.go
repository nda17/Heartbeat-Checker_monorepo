package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
)

func TestExpiredSessionRetentionSkipsAccountGuardAndBoundsBatch(t *testing.T) {
	f := newAuthFixtureWithPoolSizeAndPrefix(t, 4, "hc_auth_session_retention_")
	const email = "session-retention@example.test"
	f.signup(t, email)
	cookie, _, login := f.login(t, email, integrationPass)
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	accountID := deletionAccountID(t, f, email)
	var liveID string
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM auth.sessions WHERE account_id=$1 AND revoked_at IS NULL`, accountID).Scan(&liveID); err != nil {
		t.Fatal(err)
	}
	if cookie == nil {
		t.Fatal("login returned no session cookie")
	}
	guardedID, err := outbox.ID()
	if err != nil {
		t.Fatal(err)
	}
	guardedHash := sha256.Sum256([]byte("session-retention-account-guard"))
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO auth.sessions(id,account_id,token_hash,credential_version,expires_at,user_agent)
		VALUES($1,$2,$3,1,now()-interval '1 second','session retention guard test')`, guardedID, accountID, guardedHash[:]); err != nil {
		t.Fatal(err)
	}

	// An account deletion guard owns the account row first. Retention must skip
	// this account instead of blocking while holding any session locks.
	guard, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = guard.Exec(context.Background(), `SELECT id FROM accounts.accounts WHERE id=$1 FOR UPDATE`, accountID); err != nil {
		_ = guard.Rollback(context.Background())
		t.Fatal(err)
	}
	blockedCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	retention, err := f.pool.Begin(blockedCtx)
	if err != nil {
		_ = guard.Rollback(context.Background())
		t.Fatal(err)
	}
	skipped, err := LockExpiredSessionBatchTx(blockedCtx, retention)
	if err != nil {
		_ = retention.Rollback(context.Background())
		_ = guard.Rollback(context.Background())
		t.Fatalf("retention batch while account guard held: %v", err)
	}
	if len(skipped) != 0 {
		t.Fatalf("retention selected sessions guarded by account deletion: %v", skipped)
	}
	if err = retention.Commit(blockedCtx); err != nil {
		_ = guard.Rollback(context.Background())
		t.Fatal(err)
	}
	if err = guard.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}

	const expiredCount = 503
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < expiredCount-1; n++ {
		id, idErr := outbox.ID()
		if idErr != nil {
			_ = tx.Rollback(context.Background())
			t.Fatal(idErr)
		}
		hash := sha256.Sum256([]byte(fmt.Sprintf("session-retention-%s-%d", accountID, n)))
		if _, err = tx.Exec(context.Background(), `INSERT INTO auth.sessions(id,account_id,token_hash,credential_version,expires_at,user_agent)
			VALUES($1,$2,$3,1,now()-interval '1 second','session retention test')`, id, accountID, hash[:]); err != nil {
			_ = tx.Rollback(context.Background())
			t.Fatalf("insert expired session %d: %v", n, err)
		}
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}

	for wantRemaining := expiredCount; wantRemaining > 0; {
		batch, beginErr := f.pool.Begin(context.Background())
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		ids, lockErr := LockExpiredSessionBatchTx(context.Background(), batch)
		if lockErr != nil {
			_ = batch.Rollback(context.Background())
			t.Fatal(lockErr)
		}
		wantBatch := min(500, wantRemaining)
		if len(ids) != wantBatch {
			_ = batch.Rollback(context.Background())
			t.Fatalf("retention selected %d sessions, want %d of %d remaining", len(ids), wantBatch, wantRemaining)
		}
		if err = DeleteExpiredSessionBatchTx(context.Background(), batch, ids); err != nil {
			_ = batch.Rollback(context.Background())
			t.Fatalf("delete locked expired sessions: %v", err)
		}
		if err = batch.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
		wantRemaining -= wantBatch
	}
	var expiredRemaining, liveRemaining int
	if err = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.sessions WHERE account_id=$1 AND expires_at<=now()`, accountID).Scan(&expiredRemaining); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.sessions WHERE id=$1 AND expires_at>now() AND revoked_at IS NULL`, liveID).Scan(&liveRemaining); err != nil {
		t.Fatal(err)
	}
	if expiredRemaining != 0 || liveRemaining != 1 {
		t.Fatalf("retention result expired=%d live=%d, want expired=0 live=1", expiredRemaining, liveRemaining)
	}
}
