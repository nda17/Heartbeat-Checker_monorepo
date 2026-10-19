package admin

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestAdminCannotReviveDeletedAccountWithPatch(t *testing.T) {
	f := newAdminFixture(t)
	admin := f.seedActor(t, "a2000000-0000-4000-8000-000000000001", "b2000000-0000-4000-8000-000000000001", strings.Repeat("C", 43), "Admin", "delete-admin-patch@example.test", "admin", true)
	target := f.seedActor(t, "a2000000-0000-4000-8000-000000000002", "b2000000-0000-4000-8000-000000000002", strings.Repeat("D", 43), "Old Name", "deleted-patch-target@example.test", "user", false)
	if _, err := f.pool.Exec(context.Background(), `UPDATE accounts.accounts SET name='Deleted account',status='deleted',deleted_at=now() WHERE id=$1`, target.id); err != nil {
		t.Fatal(err)
	}
	response := f.request(t, admin, http.MethodPatch, "/api/v1/admin/accounts/"+target.id, map[string]any{
		"version": 1, "status": "active", "reason": "attempt to revive deleted account",
	})
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "not_found") {
		t.Fatalf("admin PATCH revived deleted account: status=%d body=%s", response.Code, response.Body.String())
	}
	var status, name string
	if err := f.pool.QueryRow(context.Background(), `SELECT status,name FROM accounts.accounts WHERE id=$1`, target.id).Scan(&status, &name); err != nil {
		t.Fatal(err)
	}
	if status != "deleted" || name != "Deleted account" {
		t.Fatalf("failed revival request changed deleted account: status=%q name=%q", status, name)
	}
}
