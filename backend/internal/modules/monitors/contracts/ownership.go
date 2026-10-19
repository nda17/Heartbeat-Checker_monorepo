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

// NotificationChannelsReader reports enabled channels without exposing their targets.
// The caller holds its account billing lock and owns the transaction.
type NotificationChannelsReader interface {
	EnabledChannelsTx(context.Context, pgx.Tx, string, []string) (map[string][]string, error)
}
