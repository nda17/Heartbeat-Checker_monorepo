package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Create(ctx context.Context, tx pgx.Tx, name string) (contracts.Account, error) {
	id, err := newID()
	if err != nil {
		return contracts.Account{}, err
	}
	var a contracts.Account
	err = tx.QueryRow(ctx, `INSERT INTO accounts.accounts (id, name, role, status)
		VALUES ($1, $2, 'user', 'active') RETURNING id, name, role, status, created_at`, id, name).
		Scan(&a.ID, &a.Name, &a.Role, &a.Status, &a.CreatedAt)
	return a, err
}

func (r *Repository) Get(ctx context.Context, id string) (contracts.Account, error) {
	var a contracts.Account
	err := r.pool.QueryRow(ctx, `SELECT id, name, role, status, created_at FROM accounts.accounts WHERE id = $1`, id).
		Scan(&a.ID, &a.Name, &a.Role, &a.Status, &a.CreatedAt)
	return a, err
}

func (r *Repository) UpdateName(ctx context.Context, id, name string) (contracts.Account, error) {
	var a contracts.Account
	err := r.pool.QueryRow(ctx, `UPDATE accounts.accounts SET name = $2, updated_at = now()
		WHERE id = $1 AND status = 'active' RETURNING id, name, role, status, created_at`, id, name).
		Scan(&a.ID, &a.Name, &a.Role, &a.Status, &a.CreatedAt)
	return a, err
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func (r *Repository) Active(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM accounts.accounts WHERE id=$1 FOR SHARE`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return status == "active", err
}
