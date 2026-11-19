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
	amount := flag.Int("amount-minor", 0, "balance credit in kopecks")
	reason := flag.String("reason", "", "reason for this local grant")
	flag.Parse()
	databaseURL := os.Getenv("DATABASE_URL")
	if err := config.LocalGrantDatabase(databaseURL); err != nil {
		return err
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
	if err = billing.New(pool, true).CreditLocal(ctx, *account, *amount, strings.TrimSpace(*reason)); err != nil {
		return errors.New("local balance credit failed; check account and amount")
	}
	fmt.Println("Local balance credited for development.")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
