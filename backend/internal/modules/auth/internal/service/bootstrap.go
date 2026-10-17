package service

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/repository/postgres"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/security"
)

type BootstrapInput struct {
	Email    string
	Name     string
	Password string
}

// BootstrapLocal is not part of the HTTP service. The local CLI supplies the
// account and entitlement operations, keeping their SQL in the owning modules.
func BootstrapLocal(ctx context.Context, repo *postgres.Repository, input BootstrapInput,
	createAccount func(context.Context, pgx.Tx, string) (string, error),
	grant func(context.Context, pgx.Tx, string) error,
) (string, error) {
	input.Email = security.NormalizeEmail(input.Email)
	name, err := contracts.CleanName(input.Name)
	if err != nil || !security.ValidEmail(input.Email) || len(input.Password) < 12 || len(input.Password) > 1024 || repo == nil || createAccount == nil || grant == nil {
		return "", ErrInvalid
	}
	// Argon2id must finish before beginning the short database transaction.
	hash, err := security.HashPassword(input.Password)
	if err != nil {
		return "", err
	}
	return repo.BootstrapLocal(ctx, input.Email, name, hash, createAccount, grant)
}
