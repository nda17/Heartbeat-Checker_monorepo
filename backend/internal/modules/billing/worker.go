package billing

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/billing/contracts"
)

type ReportWorker struct {
	pool   *pgxpool.Pool
	sender contracts.ReportSender
	logger *slog.Logger
}

func NewReportWorker(pool *pgxpool.Pool, sender contracts.ReportSender, loggers ...*slog.Logger) (*ReportWorker, error) {
	if pool == nil || sender == nil {
		return nil, errors.New("payment report dependencies are required")
	}
	logger := slog.Default()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	return &ReportWorker{pool: pool, sender: sender, logger: logger}, nil
}

func reportFailure(err error) (string, int, time.Duration) {
	code, status, retryAfter := "provider_unavailable", 0, time.Duration(0)
	var failure *contracts.ReportSendError
	if errors.As(err, &failure) {
		switch failure.Code {
		case "provider_unavailable", "provider_http", "provider_rejected", "provider_response_invalid", "report_invalid", "request_invalid", "encode_failed":
			code = failure.Code
		}
		if failure.HTTPStatus >= 100 && failure.HTTPStatus <= 599 {
			status = failure.HTTPStatus
		}
		if failure.RetryAfter > 0 && failure.RetryAfter <= 24*time.Hour {
			retryAfter = failure.RetryAfter
		}
	}
	return code, status, retryAfter
}

// ProcessOne delivers outside its claim transaction. After a lost acknowledgement a
// report may repeat; OrderID lets an adapter identify that duplicate explicitly.
func (w *ReportWorker) ProcessOne(ctx context.Context) (bool, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return false, err
	}
	token := fmt.Sprintf("%x-%x-%x-%x-%x", nonce[:4], nonce[4:6], nonce[6:8], nonce[8:10], nonce[10:])
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	// Finalize exhausted leases before finding another job, without rolling this
	// update back when the remaining queue is empty.
	if _, err = tx.Exec(ctx, `UPDATE billing.payment_reports SET status='failed',attempt_token=NULL,lease_until=NULL,finished_at=now(),last_error_code='delivery_unconfirmed',last_http_status=NULL WHERE status='sending' AND lease_until<=now() AND attempts>=8`); err != nil {
		return false, err
	}
	var report contracts.PaymentReport
	var attempt int
	err = tx.QueryRow(ctx, `SELECT r.order_id,o.account_id,o.monitor_count,o.amount_minor,o.currency,o.completed_at,r.attempts,o.provider,o.fulfillment_status,(o.provider_mode='test') FROM billing.payment_reports r JOIN billing.orders o ON o.id=r.order_id WHERE r.attempts<8 AND ((r.status='pending' AND r.next_attempt_at<=now()) OR (r.status='sending' AND r.lease_until<=now())) ORDER BY r.next_attempt_at,r.order_id FOR UPDATE OF r SKIP LOCKED LIMIT 1`).Scan(&report.OrderID, &report.AccountID, &report.MonitorCount, &report.AmountMinor, &report.Currency, &report.CompletedAt, &attempt, &report.Provider, &report.FulfillmentStatus, &report.Test)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE billing.payment_reports SET status='sending',attempts=attempts+1,attempt_token=$2,lease_until=now()+interval '1 minute' WHERE order_id=$1`, report.OrderID, token); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	sendErr := w.sender.SendPaymentReport(sendCtx, report)
	cancel()
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	status := "sent"
	delay := time.Duration(1<<attempt) * 30 * time.Second
	code, httpStatus, retryAfter := "", 0, time.Duration(0)
	if sendErr != nil {
		code, httpStatus, retryAfter = reportFailure(sendErr)
		status = "pending"
		if attempt+1 >= 8 {
			status = "failed"
		}
		if retryAfter > delay {
			delay = retryAfter
		}
	}
	tag, err := w.pool.Exec(finishCtx, `UPDATE billing.payment_reports SET status=$3,next_attempt_at=now()+$4*interval '1 second',attempt_token=NULL,lease_until=NULL,finished_at=CASE WHEN $3 IN ('sent','failed') THEN now() ELSE NULL END,last_error_code=NULLIF($5,''),last_http_status=NULLIF($6::integer,0) WHERE order_id=$1 AND status='sending' AND attempt_token=$2 AND lease_until>now()`, report.OrderID, token, status, delay.Seconds(), code, httpStatus)
	if err == nil && tag.RowsAffected() == 1 && status == "failed" {
		w.logger.Warn("payment report delivery exhausted", "order_id", report.OrderID, "error_code", code, "http_status", httpStatus)
	}
	return true, err
}
