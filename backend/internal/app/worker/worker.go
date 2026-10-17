package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/config"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/outbox"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/postgres"
	probeclient "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/client"
	probe "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe/contracts"
)

func Run(ctx context.Context, cfg config.WorkerConfig, logger *slog.Logger) error {
	pool, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.DatabaseTimeout)
	if err != nil {
		return err
	}
	defer pool.Close()
	client, err := probeclient.New(cfg.ProbeURL, cfg.ProbeSharedSecret)
	if err != nil {
		return err
	}
	incidentModule := incidents.New(pool)
	monitorModule := monitors.New(pool, billing.New(pool, cfg.AppEnv == "local"), incidentModule, accounts.New(pool))
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				attempt, err := monitorModule.Claim(claimCtx)
				cancel()
				if err != nil {
					if ctx.Err() == nil {
						logger.Error("claim monitoring check failed")
					}
					continue
				}
				if attempt == nil {
					continue
				}
				result, err := client.Check(ctx, attempt.Request)
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					result = probe.Result{RunID: attempt.Request.RunID, AttemptToken: attempt.Request.AttemptToken, ConfigVersion: attempt.Request.ConfigVersion, Outcome: "UNKNOWN", CheckedAt: time.Now().UTC(), ErrorCode: "probe_unavailable"}
				}
				saveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				_, err = monitorModule.Complete(saveCtx, *attempt, result)
				cancel()
				if err != nil && ctx.Err() == nil {
					logger.Error("accept monitoring check failed", "monitor_id", attempt.MonitorID)
				}
			}
		})
	}
	wg.Go(func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			tx, err := pool.Begin(cleanupCtx)
			if err == nil {
				// Each module owns its retention SQL; the transaction bounds one cleanup batch.
				if err = auth.Cleanup(cleanupCtx, tx); err == nil {
					err = monitorModule.Cleanup(cleanupCtx, tx)
				}
				if err == nil {
					err = incidentModule.Cleanup(cleanupCtx, tx)
				}
				if err == nil {
					err = outbox.Cleanup(cleanupCtx, tx)
				}
				if err == nil {
					err = tx.Commit(cleanupCtx)
				} else {
					tx.Rollback(cleanupCtx)
				}
			}
			cancel()
			if err != nil && ctx.Err() == nil {
				logger.Error("retention cleanup failed")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
	logger.Info("monitoring worker started", "concurrency", 4)
	<-ctx.Done()
	wg.Wait()
	return nil
}
