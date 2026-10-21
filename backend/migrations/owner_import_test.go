package migrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const syntheticOwnerID = "51ec7d19-80c4-4fb6-8edb-79dc7abe3120"
const syntheticOwnerEmail = "khv1702@gmail.com"
const syntheticOwnerOperation = "owner-production-2026-10-19-v1"
const syntheticOwnerHash = "$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

type ownerImportPayload struct {
	AccountID    string `json:"accountId"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	PasswordHash string `json:"passwordHash"`
	Quota        int    `json:"quota"`
	ValidUntil   string `json:"validUntil"`
}

func validSyntheticOwnerPayload() ownerImportPayload {
	return ownerImportPayload{
		AccountID: syntheticOwnerID, Name: "Synthetic Production Owner", Email: syntheticOwnerEmail,
		PasswordHash: syntheticOwnerHash, Quota: 1000, ValidUntil: "2027-10-18T00:00:00Z",
	}
}

// runOwnerImport executes the tracked one-off SQL template against the
// disposable test database. The COPY row contains only hex/digest/SHA fields.
func runOwnerImport(ctx context.Context, t *testing.T, pool *pgxpool.Pool, payload ownerImportPayload, claimedDigest, sourceSHA string) (string, error) {
	t.Helper()
	template, err := os.ReadFile("../../deploy/backend/import-owner.sql")
	if err != nil {
		return "", fmt.Errorf("read tracked owner import SQL: %w", err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	row := fmt.Sprintf("INSERT INTO owner_import_input VALUES ('%s','%s','%s');", hex.EncodeToString(encoded), claimedDigest, sourceSHA)
	copyBlock := "COPY owner_import_input FROM STDIN;\n__OWNER_PAYLOAD_ROW__\n\\."
	if strings.Count(string(template), copyBlock) != 1 {
		return "", fmt.Errorf("tracked owner import SQL COPY input marker changed")
	}
	script := strings.Replace(string(template), copyBlock, row, 1)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Release()
	results, err := conn.Conn().PgConn().Exec(ctx, script).ReadAll()
	if err != nil {
		_, rollbackErr := conn.Exec(ctx, "ROLLBACK")
		if rollbackErr != nil {
			return "", fmt.Errorf("owner SQL failed: %v; rollback failed: %w", err, rollbackErr)
		}
		return "", err
	}
	for _, result := range results {
		if len(result.FieldDescriptions) == 1 && len(result.Rows) == 1 && len(result.Rows[0]) == 1 {
			return string(result.Rows[0][0]), nil
		}
	}
	return "", fmt.Errorf("tracked owner SQL returned no result row")
}

func payloadDigest(t *testing.T, payload ownerImportPayload) string {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func TestOwnerImportCreatesAtomicallyAndExactReplayDoesNotOverwriteChanges(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	if err := Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	payload := validSyntheticOwnerPayload()
	digest := payloadDigest(t, payload)
	created, err := runOwnerImport(ctx, t, pool, payload, digest, strings.Repeat("a", 40))
	if err != nil || created != "created|"+strings.Repeat("a", 40) {
		t.Fatalf("initial import result=%q err=%v", created, err)
	}
	var name, role, status, email, passwordHash, source, reason string
	var quota int
	if err := pool.QueryRow(ctx, `SELECT a.name,a.role,a.status,c.email,c.password_hash,'admin_grant',e.reason,e.monitor_limit
		FROM accounts.accounts a JOIN auth.credentials c ON c.account_id=a.id JOIN billing.admin_grants e ON e.account_id=a.id WHERE a.id=$1`, syntheticOwnerID).
		Scan(&name, &role, &status, &email, &passwordHash, &source, &reason, &quota); err != nil {
		t.Fatalf("read imported owner records: %v", err)
	}
	if name != payload.Name || role != "admin" || status != "active" || email != payload.Email || passwordHash != payload.PasswordHash || source != "admin_grant" || quota != payload.Quota || reason == "" {
		t.Fatalf("owner import state name=%q role=%q status=%q email=%q hashMatches=%t source=%q quota=%d reason=%q", name, role, status, email, passwordHash == payload.PasswordHash, source, quota, reason)
	}
	var auditCount int
	var markerDigest, markerSource string
	if err := pool.QueryRow(ctx, `SELECT count(*),max(metadata->>'payloadSha256'),max(metadata->>'sourceSha') FROM admin.audit_events WHERE operation_id=$1`, syntheticOwnerOperation).Scan(&auditCount, &markerDigest, &markerSource); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 || markerDigest != digest || markerSource != strings.Repeat("a", 40) {
		t.Fatalf("audit marker count=%d digest=%q source=%q", auditCount, markerDigest, markerSource)
	}

	// Later administrative edits are authoritative. Replaying the exact
	// payload is a no-op, even if the deploy source SHA has changed.
	if _, err := pool.Exec(ctx, `UPDATE accounts.accounts SET name='Later Name',role='user' WHERE id=$1`, syntheticOwnerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE auth.credentials SET password_hash='later-password-hash' WHERE account_id=$1`, syntheticOwnerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE billing.admin_grants SET monitor_limit=12,reason='later grant adjustment' WHERE account_id=$1`, syntheticOwnerID); err != nil {
		t.Fatal(err)
	}
	noop, err := runOwnerImport(ctx, t, pool, payload, digest, strings.Repeat("b", 40))
	if err != nil || noop != "noop|"+strings.Repeat("a", 40) {
		t.Fatalf("exact replay result=%q err=%v", noop, err)
	}
	if err := pool.QueryRow(ctx, `SELECT a.name,a.role,c.password_hash,e.monitor_limit,e.reason
		FROM accounts.accounts a JOIN auth.credentials c ON c.account_id=a.id JOIN billing.admin_grants e ON e.account_id=a.id WHERE a.id=$1`, syntheticOwnerID).
		Scan(&name, &role, &passwordHash, &quota, &reason); err != nil {
		t.Fatal(err)
	}
	if name != "Later Name" || role != "user" || passwordHash != "later-password-hash" || quota != 12 || reason != "later grant adjustment" {
		t.Fatalf("exact replay overwrote later edits: name=%q role=%q hash=%q quota=%d reason=%q", name, role, passwordHash, quota, reason)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM admin.audit_events WHERE operation_id=$1`, syntheticOwnerOperation).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("exact replay changed audit marker count=%d err=%v", auditCount, err)
	}

	changed := payload
	changed.PasswordHash = "$argon2id$v=19$m=19456,t=2,p=1$CCCCCCCCCCCCCCCCCCCCCC$DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"
	if _, err := runOwnerImport(ctx, t, pool, changed, payloadDigest(t, changed), strings.Repeat("c", 40)); err == nil {
		t.Fatal("changed password payload unexpectedly replayed an existing operation")
	}
	if _, err := runOwnerImport(ctx, t, pool, payload, strings.Repeat("0", 64), strings.Repeat("d", 40)); err == nil {
		t.Fatal("wrong claimed payload digest unexpectedly replayed an existing operation")
	}
}

func TestOwnerImportIdentityAndAuditFailuresRollBackWholeTransaction(t *testing.T) {
	pool := testDatabase(t)
	ctx := context.Background()
	if err := Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		seed func() error
	}{
		{name: "uuid conflict", seed: func() error {
			_, err := pool.Exec(ctx, `INSERT INTO accounts.accounts(id,name) VALUES($1,'Preexisting target UUID')`, syntheticOwnerID)
			return err
		}},
		{name: "email conflict", seed: func() error {
			_, err := pool.Exec(ctx, `INSERT INTO accounts.accounts(id,name) VALUES('51ec7d19-80c4-4fb6-8edb-79dc7abe3130','Existing email owner')`)
			if err != nil {
				return err
			}
			_, err = pool.Exec(ctx, `INSERT INTO auth.credentials(account_id,email,password_hash) VALUES('51ec7d19-80c4-4fb6-8edb-79dc7abe3130',$1,'existing-hash')`, syntheticOwnerEmail)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.seed(); err != nil {
				t.Fatal(err)
			}
			payload := validSyntheticOwnerPayload()
			if _, err := runOwnerImport(ctx, t, pool, payload, payloadDigest(t, payload), strings.Repeat("e", 40)); err == nil {
				t.Fatal("identity conflict did not abort owner import")
			}
			var ownerExists bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts.accounts WHERE id=$1)`, syntheticOwnerID).Scan(&ownerExists); err != nil {
				t.Fatal(err)
			}
			if tc.name == "email conflict" && ownerExists {
				t.Fatal("email conflict left a partial owner account")
			}
			var markerCount int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM admin.audit_events WHERE operation_id=$1`, syntheticOwnerOperation).Scan(&markerCount); err != nil || markerCount != 0 {
				t.Fatalf("identity conflict left audit marker count=%d err=%v", markerCount, err)
			}
			if tc.name == "uuid conflict" {
				if _, err := pool.Exec(ctx, `DELETE FROM accounts.accounts WHERE id=$1`, syntheticOwnerID); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "email conflict" {
				if _, err := pool.Exec(ctx, `DELETE FROM accounts.accounts WHERE id='51ec7d19-80c4-4fb6-8edb-79dc7abe3130'`); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION public.reject_owner_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.operation_id='owner-production-2026-10-19-v1' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TRIGGER reject_owner_audit BEFORE INSERT ON admin.audit_events FOR EACH ROW EXECUTE FUNCTION public.reject_owner_audit()`); err != nil {
		t.Fatal(err)
	}
	payload := validSyntheticOwnerPayload()
	if _, err := runOwnerImport(ctx, t, pool, payload, payloadDigest(t, payload), strings.Repeat("f", 40)); err == nil {
		t.Fatal("synthetic audit failure did not abort owner import")
	}
	var ownerRows, credentialRows, entitlementRows, auditRows int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM accounts.accounts WHERE id=$1),
		(SELECT count(*) FROM auth.credentials WHERE account_id=$1),
		(SELECT count(*) FROM billing.admin_grants WHERE account_id=$1),
		(SELECT count(*) FROM admin.audit_events WHERE operation_id=$2)`, syntheticOwnerID, syntheticOwnerOperation).
		Scan(&ownerRows, &credentialRows, &entitlementRows, &auditRows); err != nil {
		t.Fatal(err)
	}
	if ownerRows != 0 || credentialRows != 0 || entitlementRows != 0 || auditRows != 0 {
		t.Fatalf("audit failure left partial rows account=%d credential=%d entitlement=%d audit=%d", ownerRows, credentialRows, entitlementRows, auditRows)
	}
}
