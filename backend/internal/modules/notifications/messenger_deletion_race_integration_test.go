package notifications

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMessengerDisconnectWaitsForAccountDeletionBeforeLockingDestination(t *testing.T) {
	const label = "delete-disconnect-race"
	f := newMessengerAPIFixture(t, true, label)
	actor := f.actors[label]
	destinationID := "20000000-0000-4000-8000-000000000001"
	fingerprint := make([]byte, 32)
	for i := range fingerprint {
		fingerprint[i] = byte(i + 1)
	}
	if _, err := f.webhookFixture.pool.Exec(context.Background(), `INSERT INTO notifications.messenger_destinations
		(id,account_id,provider,kind,recipient_fingerprint,user_fingerprint,encrypted_target)
		VALUES ($1,$2,'telegram','private',$3,$4,'synthetic ciphertext')`, destinationID, actor.accountID, fingerprint, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := f.webhookFixture.pool.Exec(context.Background(), `INSERT INTO notifications.messenger_routes
		(id,account_id,monitor_id,provider,destination_id,enabled)
		VALUES ('20000000-0000-4000-8000-000000000002',$1,$2,'telegram',$3,true)`, actor.accountID, f.monitors[label], destinationID); err != nil {
		t.Fatal(err)
	}

	tx, err := f.webhookFixture.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var locked string
	if err := tx.QueryRow(context.Background(), `SELECT id FROM accounts.accounts WHERE id=$1 FOR NO KEY UPDATE`, actor.accountID).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `UPDATE accounts.accounts SET status='deleted',deleted_at=now(),name='Deleted account' WHERE id=$1`, actor.accountID); err != nil {
		t.Fatal(err)
	}

	disconnect := make(chan error, 1)
	go func() {
		disconnect <- f.module.Disconnect(context.Background(), actor.accountID, destinationID)
	}()
	deadline := time.Now().Add(5 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		var found bool
		if err := f.webhookFixture.pool.QueryRow(context.Background(), `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND state='active'
			AND query LIKE 'SELECT a.status=%accounts.accounts a WHERE a.id=%FOR SHARE%')`).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found {
			waiting = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("disconnect never queued on the account row lock")
	}

	if _, err := tx.Exec(context.Background(), `DELETE FROM monitors.monitors WHERE account_id=$1`, actor.accountID); err != nil {
		t.Fatal(err)
	}
	if err := DeleteAccountTx(context.Background(), tx, actor.accountID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-disconnect:
		if !errors.Is(err, ErrMessengerNotFound) {
			t.Fatalf("disconnect result after deletion = %v, want ErrMessengerNotFound", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect did not finish after the deletion transaction committed")
	}
	var destinations, routes int
	if err := f.webhookFixture.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.messenger_destinations WHERE account_id=$1`, actor.accountID).Scan(&destinations); err != nil {
		t.Fatal(err)
	}
	if err := f.webhookFixture.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications.messenger_routes WHERE account_id=$1`, actor.accountID).Scan(&routes); err != nil {
		t.Fatal(err)
	}
	if destinations != 0 || routes != 0 {
		t.Fatalf("deletion left messenger state: destinations=%d routes=%d", destinations, routes)
	}
}
