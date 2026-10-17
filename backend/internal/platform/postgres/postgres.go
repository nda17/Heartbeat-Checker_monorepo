package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context, databaseURL string, timeout time.Duration) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// Parser errors can contain credentials from malformed connection strings.
		return nil, errors.New("DATABASE_URL is not a valid PostgreSQL connection string")
	}
	cfg.ConnConfig.ConnectTimeout = timeout
	cfg.MaxConns = 10
	cfg.MinConns = 0
	cfg.PingTimeout = timeout

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("could not create PostgreSQL connection pool")
	}
	return pool, nil
}
