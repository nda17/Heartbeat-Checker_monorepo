package accounts

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/internal/repository/postgres"
)

var ErrAdminConflict = postgres.ErrAdminConflict
var ErrLastAdmin = postgres.ErrLastAdmin

var ErrInvalidName = contracts.ErrInvalidName

type Module struct {
	repo *postgres.Repository
}

func New(pool *pgxpool.Pool) *Module {
	return &Module{repo: postgres.New(pool)}
}

func (m *Module) Create(ctx context.Context, tx pgx.Tx, name string) (contracts.Account, error) {
	name, err := contracts.CleanName(name)
	if err != nil {
		return contracts.Account{}, err
	}
	return m.repo.Create(ctx, tx, name)
}

// CreateLocalAdmin is used only by the explicit local bootstrap command.
// Ordinary registration continues to use Create and always creates a user.
func (m *Module) CreateLocalAdmin(ctx context.Context, tx pgx.Tx, name string) (contracts.Account, error) {
	name, err := contracts.CleanName(name)
	if err != nil {
		return contracts.Account{}, err
	}
	return m.repo.CreateLocalAdmin(ctx, tx, name)
}

func (m *Module) Get(ctx context.Context, id string) (contracts.Account, error) {
	return m.repo.Get(ctx, id)
}

func (m *Module) UpdateName(ctx context.Context, id, name string) (contracts.Account, error) {
	name, err := contracts.CleanName(name)
	if err != nil {
		return contracts.Account{}, err
	}
	return m.repo.UpdateName(ctx, id, name)
}

func (m *Module) Active(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	return m.repo.Active(ctx, tx, id)
}

func (m *Module) TryActive(ctx context.Context, tx pgx.Tx, id string) (bool, bool, error) {
	return m.repo.TryActive(ctx, tx, id)
}

func (m *Module) LockAdminAccount(ctx context.Context, tx pgx.Tx, id string) (contracts.Account, error) {
	return m.repo.LockAdminAccount(ctx, tx, id)
}
func (m *Module) AdminUpdate(ctx context.Context, tx pgx.Tx, current contracts.Account, version int64, name, role, status *string) (contracts.Account, bool, error) {
	return m.repo.AdminUpdate(ctx, tx, current, version, name, role, status)
}

func (m *Module) GetForAuthTx(ctx context.Context, tx pgx.Tx, id string) (contracts.Account, error) {
	return m.repo.GetForAuthTx(ctx, tx, id)
}
