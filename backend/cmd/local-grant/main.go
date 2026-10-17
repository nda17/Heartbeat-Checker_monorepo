package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/config"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/postgres"
)

func run() error {
	account := flag.String("account-id", "", "existing account UUID")
	limit := flag.String("limit", "", "configured monitor quota")
	until := flag.String("until", "", "expiration RFC3339")
	reason := flag.String("reason", "", "reason for this local grant")
	flag.Parse()
	databaseURL := os.Getenv("DATABASE_URL")
	if err := config.LocalGrantDatabase(databaseURL); err != nil {
		return err
	}
	quota, err := config.GrantQuota(*limit)
	if err != nil {
		return err
	}
	expiration, err := time.Parse(time.RFC3339, *until)
	if err != nil {
		return errors.New("until must be RFC3339")
	}
	if *account == "" || strings.TrimSpace(*reason) == "" {
		return errors.New("account-id and reason are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, databaseURL, 5*time.Second)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err = billing.New(pool, true).GrantLocal(ctx, *account, quota, expiration, strings.TrimSpace(*reason)); err != nil {
		return errors.New("local grant failed; check account, period and quota")
	}
	fmt.Println("Local test entitlement granted; this is not a payment.")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
