package service

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/backend/internal/modules/monitors/internal/provider"
)

// StartProviderWorker starts independent refresh and one-at-a-time enrichment
// loops. No dataset failure delays monitoring startup or consumes queue attempts.
func (s *Service) StartProviderWorker(ctx context.Context, wg *sync.WaitGroup, logger *slog.Logger, directory string) {
	var current atomic.Pointer[provider.Dataset]
	cache := provider.Cache{Directory: directory}
	wg.Go(func() {
		if dataset, err := cache.Load(); err == nil {
			current.Store(dataset)
			logger.Info("ASN cache loaded", "dataset_version", dataset.Version)
		}
		delay := time.Duration(0)
		for {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			dataset, err := cache.Refresh(refreshCtx)
			cancel()
			if err == nil {
				current.Store(dataset)
				logger.Info("ASN snapshot refreshed", "dataset_version", dataset.Version)
				delay = 24 * time.Hour
			} else {
				if ctx.Err() == nil {
					logger.Warn("ASN snapshot refresh failed; retaining last verified cache", "error", err)
				}
				delay = time.Hour
			}
		}
	})
	wg.Go(func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			dataset := current.Load()
			if dataset == nil {
				continue
			}
			claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			a, err := s.repo.ClaimProvider(claimCtx)
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					logger.Error("claim provider lookup failed")
				}
				continue
			}
			if a == nil {
				continue
			}
			lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			ips, lookupErr := provider.Addresses(lookupCtx, net.DefaultResolver, a.Host)
			cancel()
			if ctx.Err() != nil {
				return
			}
			names, asns := []string{}, []int64{}
			if lookupErr == nil {
				names, asns = dataset.Resolve(ips)
			}
			saveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = s.repo.CompleteProvider(saveCtx, *a, names, asns, dataset.Version, lookupErr != nil)
			cancel()
			if err != nil && ctx.Err() == nil {
				logger.Error("complete provider lookup failed", "monitor_id", a.MonitorID)
			}
		}
	})
}
