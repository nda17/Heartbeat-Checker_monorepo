package main

import (
	"context"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/app/worker"
	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/platform/config"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.LoadWorker()
	if err != nil {
		logger.Error("configure worker", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err = worker.Run(ctx, cfg, logger); err != nil {
		logger.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}
