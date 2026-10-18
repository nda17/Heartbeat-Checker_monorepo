package auth

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/repository/postgres"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth/internal/service"
)

type MailWorker = service.MailWorker

func NewMailWorker(pool *pgxpool.Pool, encodedKey string, sender service.JobSender) (*MailWorker, error) {
	return service.NewMailWorker(postgres.New(pool), encodedKey, sender)
}
