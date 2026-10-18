package monitors

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type Ownership struct{}

func NewOwnership() *Ownership { return &Ownership{} }

func (o *Ownership) LockOwned(ctx context.Context, tx pgx.Tx, accountID, monitorID string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM monitors.monitors WHERE account_id=$1 AND id=$2 FOR UPDATE`, accountID, monitorID).Scan(&id)
	return err
}

func (o *Ownership) TryLockOwned(ctx context.Context, tx pgx.Tx, accountID, monitorID string) error {
	var id string
	return tx.QueryRow(ctx, `SELECT id::text FROM monitors.monitors WHERE account_id=$1 AND id=$2 FOR UPDATE SKIP LOCKED`, accountID, monitorID).Scan(&id)
}
