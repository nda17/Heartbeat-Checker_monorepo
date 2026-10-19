package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
)

var ErrAdminConflict = errors.New("account version conflict")
var ErrLastAdmin = errors.New("last active administrator")

// LockAdminAccount is called after billing.Lock in ascending account ID order.
func (r *Repository) LockAdminAccount(ctx context.Context, tx pgx.Tx, id string) (contracts.Account, error) {
	var a contracts.Account
	err := tx.QueryRow(ctx, `SELECT id,name,role,status,version,created_at FROM accounts.accounts WHERE id=$1 FOR NO KEY UPDATE`, id).
		Scan(&a.ID, &a.Name, &a.Role, &a.Status, &a.Version, &a.CreatedAt)
	return a, err
}

func (r *Repository) AdminUpdate(ctx context.Context, tx pgx.Tx, current contracts.Account, expected int64, name, role, status *string) (contracts.Account, bool, error) {
	if current.Version != expected || current.Status == "deleted" {
		return current, false, ErrAdminConflict
	}
	updated := current
	if name != nil {
		clean, err := contracts.CleanName(*name)
		if err != nil {
			return current, false, err
		}
		updated.Name = clean
	}
	if role != nil {
		if *role != "user" && *role != "admin" {
			return current, false, ErrAdminConflict
		}
		updated.Role = *role
	}
	if status != nil {
		if *status != "active" && *status != "blocked" {
			return current, false, ErrAdminConflict
		}
		updated.Status = *status
	}
	changedAccess := updated.Role != current.Role || updated.Status != current.Status
	if current.Role == "admin" && current.Status == "active" && (updated.Role != "admin" || updated.Status != "active") {
		var activeAdmins int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM accounts.accounts WHERE role='admin' AND status='active'`).Scan(&activeAdmins); err != nil {
			return current, false, err
		}
		if activeAdmins <= 1 {
			return current, false, ErrLastAdmin
		}
	}
	err := tx.QueryRow(ctx, `UPDATE accounts.accounts SET name=$2,role=$3,status=$4,version=version+1,updated_at=now()
		WHERE id=$1 AND version=$5 AND status<>'deleted' RETURNING id,name,role,status,version,created_at`, current.ID, updated.Name, updated.Role, updated.Status, expected).
		Scan(&updated.ID, &updated.Name, &updated.Role, &updated.Status, &updated.Version, &updated.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return current, false, ErrAdminConflict
	}
	return updated, changedAccess, err
}
