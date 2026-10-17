package contracts

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Lock serializes mutations and monitoring acceptance for a single account.
// Call before locking a monitor, including when no entitlement exists.
type Service interface {
	Lock(context.Context, pgx.Tx, string) (Entitlement, error)
}
