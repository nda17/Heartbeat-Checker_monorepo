package api

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	accountcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/admin"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications"
)

type accountDeletion struct {
	accounts *accounts.Module
	billing  *billing.Module
	monitors *monitors.Module
}

func (d accountDeletion) LockAccountTx(ctx context.Context, tx pgx.Tx, accountID string) (accountcontracts.Account, error) {
	if _, err := d.billing.Lock(ctx, tx, accountID); err != nil {
		return accountcontracts.Account{}, err
	}
	return d.accounts.LockAdminAccount(ctx, tx, accountID)
}

func (d accountDeletion) ScrubAccountTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	if err := d.billing.DeleteAccountTx(ctx, tx, accountID); err != nil {
		return err
	}
	if err := d.monitors.DeleteAccountTx(ctx, tx, accountID); err != nil {
		return err
	}
	if err := notifications.DeleteAccountTx(ctx, tx, accountID); err != nil {
		return err
	}
	if err := admin.ScrubAccountTx(ctx, tx, accountID); err != nil {
		return err
	}
	return d.accounts.TombstoneTx(ctx, tx, accountID)
}
