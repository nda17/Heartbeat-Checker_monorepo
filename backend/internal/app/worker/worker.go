package worker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/accounts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/auth"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/incidents"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications"
	notificationcontracts "github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/notifications/contracts"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/support"
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
	accountModule := accounts.New(pool)
	notificationModule, err := notifications.New(pool, cfg.WebhookEncryptionKey, monitors.NewOwnership(), accountModule)
	if err != nil {
		return err
	}
	incidentModule := incidents.New(pool, notifications.NewRecorder())
	billingModule, err := billing.NewConfigured(pool, cfg.AppEnv == "local", billing.Config{Provider: cfg.BillingProvider, MockAccountIDs: cfg.BillingMockAccountIDs}, accountModule)
	if err != nil {
		return err
	}
	monitorModule := monitors.New(pool, billingModule, incidentModule, accountModule)
	var supportModule *support.Module
	if cfg.SupportEnabled {
		supportModule, err = support.New(pool, support.Config{EncryptionKey: cfg.SupportEncryptionKey, TelegramToken: cfg.TelegramSupportToken, MAXToken: cfg.MAXSupportToken, InternalChatID: cfg.TelegramInternalChatID, SupportTopicID: cfg.TelegramSupportTopicID, MAXSupportChatID: cfg.MAXSupportChatID, TelegramAPIIP: cfg.TelegramAPIIP, TelegramAPIBaseURL: cfg.TelegramAPIBaseURL, MAXRootCAFile: cfg.SupportMAXRootCAFile})
		if err != nil {
			return err
		}
	}
	var reportWorker *billing.ReportWorker
	if cfg.BillingReportsEnabled {
		reportWorker, err = billing.NewReportWorker(pool, newPaymentReporter(cfg.IntegrationConfig), logger)
		if err != nil {
			return err
		}
	}
	var wg sync.WaitGroup
	if supportModule != nil {
		startDeliveryLoop(ctx, &wg, logger, "support", supportModule.ProcessOne)
	}
	if reportWorker != nil {
		startDeliveryLoop(ctx, &wg, logger, "payment report", reportWorker.ProcessOne)
	}
	if cfg.AppEnv == "production" {
		mailer, err := auth.NewProductionMailer(cfg.SMTPAddr, cfg.SMTPFrom, cfg.SMTPUsername, cfg.SMTPPassword, cfg.SMTPTLSMode)
		if err != nil {
			return err
		}
		mailWorker, err := auth.NewMailWorker(pool, cfg.AuthMailEncryptionKey, mailer)
		if err != nil {
			return err
		}
		wg.Go(func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				_, err := mailWorker.ProcessOne(attemptCtx)
				cancel()
				if err != nil && ctx.Err() == nil {
					logger.Error("auth mail delivery attempt failed")
				}
			}
		})
	}
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
	for range 2 {
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
				attempt, err := notificationModule.Claim(claimCtx)
				cancel()
				if err != nil {
					if ctx.Err() == nil {
						logger.Error("claim webhook delivery failed")
					}
					continue
				}
				if attempt == nil {
					continue
				}
				timestamp, signature, err := signWebhookAttempt(attempt.Secret, attempt.EventID, attempt.Body, time.Now())
				if err != nil {
					logger.Error("decode webhook signing secret failed", "delivery_id", attempt.ID)
					continue
				}
				result, err := client.DeliverWebhook(ctx, probe.WebhookRequest{URL: attempt.URL, Body: attempt.Body, EventID: attempt.EventID, Timestamp: timestamp, Signature: signature})
				if ctx.Err() != nil {
					return
				}
				outcome := notificationcontracts.Result{HTTPStatus: result.HTTPStatus, ErrorCode: result.ErrorCode, RetryAfterSeconds: result.RetryAfterSeconds}
				if err != nil {
					outcome = notificationcontracts.Result{ErrorCode: "probe_unavailable"}
				}
				saveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				_, err = notificationModule.Complete(saveCtx, *attempt, outcome)
				cancel()
				if err != nil && ctx.Err() == nil {
					logger.Error("complete webhook delivery failed", "delivery_id", attempt.ID)
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
					err = notificationModule.Cleanup(cleanupCtx, tx)
				}
				if err == nil && supportModule != nil {
					err = supportModule.Cleanup(cleanupCtx, tx)
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
	logger.Info("monitoring and webhook worker started", "monitor_concurrency", 4, "webhook_concurrency", 2)
	<-ctx.Done()
	wg.Wait()
	return nil
}

func signWebhookAttempt(encodedSecret, eventID string, body []byte, now time.Time) (string, string, error) {
	key, err := base64.RawURLEncoding.DecodeString(encodedSecret)
	if err != nil || len(key) != 32 {
		return "", "", errors.New("invalid webhook signing secret")
	}
	timestamp := strconv.FormatInt(now.UTC().Unix(), 10)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(timestamp + "." + eventID + "."))
	mac.Write(body)
	return timestamp, "v1=" + hex.EncodeToString(mac.Sum(nil)), nil
}

func startDeliveryLoop(ctx context.Context, wg *sync.WaitGroup, logger *slog.Logger, name string, process func(context.Context) (bool, error)) {
	wg.Go(func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			attemptCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			_, err := process(attemptCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				logger.Error(name + " delivery attempt failed")
			}
		}
	})
}
