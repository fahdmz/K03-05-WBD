package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"
	"github.com/fahdmz/K03-05-WBD/internal/obs"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Worker struct {
	platform     *pgxpool.Pool
	manager      dbmanager.Manager
	id           string
	pollInterval time.Duration
}

func New(platform *pgxpool.Pool, manager dbmanager.Manager) *Worker {
	return &Worker{
		platform:     platform,
		manager:      manager,
		id:           newID(),
		pollInterval: time.Second,
	}
}

func (w *Worker) ID() string {
	return w.id
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.tick(ctx); err != nil {
				obs.Logger(ctx).Error("worker.tick_failed", "worker_id", w.id, "error", obs.Redact(err.Error()))
			}
		}
	}
}

func (w *Worker) tick(ctx context.Context) error {
	return nil
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	host, _ := os.Hostname()
	return host + "-" + hex.EncodeToString(b)
}
