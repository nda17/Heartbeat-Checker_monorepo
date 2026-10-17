package accounts

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/internal/repository/postgres"
)

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
