package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/config"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/postgres"
)

// readPasswordFile accepts exact password bytes: whitespace and newlines are
// significant. O_NOFOLLOW and O_NONBLOCK also reject symlink/FIFO replacement.
func readPasswordFile(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("could not open password file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("password file must be a regular file with mode 0600")
	}
	password, err := io.ReadAll(io.LimitReader(file, 1025))
	if err != nil || len(password) < 12 || len(password) > 1024 {
		clear(password)
		return nil, errors.New("password file must contain 12 to 1024 bytes")
	}
	return password, nil
}

func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("local-admin", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	email := flags.String("email", "", "new account email")
	name := flags.String("name", "", "account display name")
	quota := flags.Int("quota", 0, "local test monitor quota, 1 to 1000")
	until := flags.String("until", "", "explicit expiration in RFC3339")
	passwordPath := flags.String("password-file", "", "regular mode-0600 file containing exact password bytes")
	reason := flags.String("reason", "", "reason for this local test entitlement")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(stdout)
			flags.PrintDefaults()
			return nil
		}
		return errors.New("invalid local-admin arguments; use --help")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	// Reject other environments and database targets before reading any secret.
	if err := config.LocalGrantDatabase(databaseURL); err != nil {
		return err
	}
	expiration, err := time.Parse(time.RFC3339, *until)
	if err != nil || !expiration.After(time.Now()) {
		return errors.New("until must be a future RFC3339 timestamp")
	}
	if *quota < 1 || *quota > 1000 {
		return errors.New("quota must be between 1 and 1000")
	}
	grantReason := strings.TrimSpace(*reason)
	if *email == "" || *name == "" || *passwordPath == "" || len(grantReason) < 1 || len(grantReason) > 500 {
		return errors.New("email, name, password-file and a reason of 1 to 500 bytes are required")
	}
	password, err := readPasswordFile(*passwordPath)
	if err != nil {
		return err
	}
	defer clear(password)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, databaseURL, 5*time.Second)
	if err != nil {
		return err
	}
	defer pool.Close()
	accountModule := accounts.New(pool)
	billingModule := billing.New(pool, true)
	_, err = auth.BootstrapLocal(ctx, pool, auth.BootstrapInput{Email: *email, Name: *name, Password: string(password)},
		func(ctx context.Context, tx pgx.Tx, name string) (string, error) {
			account, err := accountModule.CreateLocalAdmin(ctx, tx, name)
			return account.ID, err
		},
		func(ctx context.Context, tx pgx.Tx, accountID string) error {
			return billingModule.GrantLocalTx(ctx, tx, accountID, *quota, expiration, grantReason)
		})
	if errors.Is(err, auth.ErrAlreadyExists) {
		return errors.New("account already exists; nothing changed")
	}
	if errors.Is(err, auth.ErrInvalidBootstrap) {
		return errors.New("invalid account email, name or password")
	}
	if err != nil {
		return errors.New("local admin creation failed; existing accounts are never changed")
	}
	fmt.Fprintf(stdout, "Local admin account created with a local test entitlement: quota=%d until=%s; this is not a payment.\n", *quota, expiration.Format(time.RFC3339))
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
