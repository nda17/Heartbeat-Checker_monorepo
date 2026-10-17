package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/probe"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	addr := os.Getenv("PROBE_HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8090"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := probe.Run(ctx, addr, os.Getenv("PROBE_SHARED_SECRET"), logger); err != nil {
		logger.Error("probe stopped", "error", err)
		os.Exit(1)
	}
}
