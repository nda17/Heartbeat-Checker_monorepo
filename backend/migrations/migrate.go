package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.sql
var files embed.FS

// Apply runs checked-in migrations in one transaction under a database-wide lock.
// Any changed or removed migration is rejected instead of silently reapplied.
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(7349281721)); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.schema_migrations (
		version text PRIMARY KEY,
		checksum text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	rows, err := tx.Query(ctx, "SELECT version, checksum FROM public.schema_migrations")
	if err != nil {
		return fmt.Errorf("read migration ledger: %w", err)
	}
	applied := make(map[string]string)
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			return fmt.Errorf("read migration ledger: %w", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read migration ledger: %w", err)
	}
	rows.Close()

	names, err := fs.Glob(files, "*.sql")
	if err != nil || len(names) == 0 {
		return errors.New("no embedded migrations")
	}
	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		data, err := files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", version, err)
		}
		sum := sha256.Sum256(data)
		checksum := hex.EncodeToString(sum[:])
		if old, ok := applied[version]; ok {
			if old != checksum {
				return fmt.Errorf("migration %s checksum differs from applied version", version)
			}
			delete(applied, version)
			continue
		}
		if _, err := tx.Exec(ctx, string(data)); err != nil {
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO public.schema_migrations (version, checksum) VALUES ($1, $2)", version, checksum); err != nil {
			return fmt.Errorf("record migration %s: %w", version, err)
		}
	}
	for version := range applied {
		return fmt.Errorf("applied migration %s is missing from code", version)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}
