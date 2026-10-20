package notifications

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	accounts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts/contracts"
	monitors "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications/internal/service"
	notificationhttp "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications/internal/transport/http"
)

type Module struct {
	service *service.Service
	server  *notificationhttp.Server
}

// Channels combines module-owned readers without moving provider-specific
// eligibility or private targets into the API composition root.
type Channels []monitors.NotificationChannelsReader

func (readers Channels) EnabledChannelsTx(ctx context.Context, tx pgx.Tx, accountID string, monitorIDs []string) (map[string][]string, error) {
	result := make(map[string][]string, len(monitorIDs))
	for _, id := range monitorIDs {
		result[id] = []string{}
	}
	for _, reader := range readers {
		channels, err := reader.EnabledChannelsTx(ctx, tx, accountID, monitorIDs)
		if err != nil {
			return nil, err
		}
		for _, id := range monitorIDs {
			for _, channel := range channels[id] {
				duplicate := false
				for _, existing := range result[id] {
					if existing == channel {
						duplicate = true
						break
					}
				}
				if !duplicate {
					result[id] = append(result[id], channel)
				}
			}
		}
	}
	return result, nil
}

func New(pool *pgxpool.Pool, key string, owners monitors.OwnershipGuard, accounts accounts.ActiveGuard) (*Module, error) {
	s, err := service.New(pool, key, owners, accounts)
	if err != nil {
		return nil, err
	}
	return &Module{service: s, server: notificationhttp.New(s)}, nil
}
func (m *Module) Register(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	m.server.Register(mux, protect)
}
func (m *Module) Claim(ctx context.Context) (*contracts.Attempt, error) { return m.service.Claim(ctx) }
func (m *Module) Complete(ctx context.Context, a contracts.Attempt, r contracts.Result) (bool, error) {
	return m.service.Complete(ctx, a, r)
}
func (m *Module) Cleanup(ctx context.Context, tx pgx.Tx) error { return m.service.Cleanup(ctx, tx) }
