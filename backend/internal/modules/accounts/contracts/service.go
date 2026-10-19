package contracts

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var ErrInvalidName = errors.New("invalid account name")

func CleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 100 {
		return "", ErrInvalidName
	}
	return name, nil
}

type Account struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
}

type Service interface {
	Create(context.Context, pgx.Tx, string) (Account, error)
	Get(context.Context, string) (Account, error)
	GetForAuthTx(context.Context, pgx.Tx, string) (Account, error)
	UpdateName(context.Context, string, string) (Account, error)
}

// ActiveGuard checks account status using the caller's transaction, avoiding a
// second pool connection while the worker holds a lease or entitlement lock.
type ActiveGuard interface {
	Active(context.Context, pgx.Tx, string) (bool, error)
	// TryActive returns available=false when the row is locked or absent.
	TryActive(context.Context, pgx.Tx, string) (bool, bool, error)
}
