package contracts

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// OwnershipGuard locks an account's monitor in the caller's transaction.
type OwnershipGuard interface {
	LockOwned(context.Context, pgx.Tx, string, string) error
	// TryLockOwned skips a monitor row held by another transaction.
	TryLockOwned(context.Context, pgx.Tx, string, string) error
}
