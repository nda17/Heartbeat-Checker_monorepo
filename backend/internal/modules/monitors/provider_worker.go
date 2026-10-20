package monitors

import (
	"context"
	"log/slog"
	"sync"
)

func (m *Module) StartProviderWorker(ctx context.Context, wg *sync.WaitGroup, logger *slog.Logger, cacheDirectory string) {
	m.service.StartProviderWorker(ctx, wg, logger, cacheDirectory)
}
