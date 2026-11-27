package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"
	"github.com/fahdmz/K03-05-WBD/internal/metrics"
	"github.com/fahdmz/K03-05-WBD/internal/obs"
	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/fahdmz/K03-05-WBD/internal/worker"
	"github.com/joho/godotenv"
)

func main() {
	godotenv.Load()
	shutdown, err := obs.Setup("worker")
	if err != nil {
		fatal("obs.setup_failed", err)
	}
	defer shutdown(context.Background())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		fatal("worker.db_connect_failed", err)
	}
	defer db.Close()

	manager, err := dbmanager.NewPostgresManagerFromEnv(db)
	if err != nil {
		fatal("worker.manager_failed", err)
	}

	n := 4
	if s := os.Getenv("WORKER_CONCURRENCY"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 1 {
			slog.Warn("worker.bad_concurrency", "value", s, "using", n)
		} else {
			n = v
		}
	}

	w := worker.New(db, manager)
	w.SetConcurrency(n)

	// bonus observability: metrics Prometheus di :9091/metrics
	reg := metrics.New()
	w.SetMetrics(worker.NewMetrics(reg, db))
	addr := os.Getenv("WORKER_METRICS_ADDR")
	if addr == "" {
		addr = ":9091"
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", reg.Handler())
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("worker.metrics_failed", "addr", addr, "error", err.Error())
		}
	}()
	defer srv.Shutdown(context.Background())

	slog.Info("worker.started", "worker_id", w.ID(), "concurrency", n)
	w.Run(ctx)
	slog.Info("worker.stopped", "worker_id", w.ID())
}

func fatal(event string, err error) {
	slog.Error(event, "error", obs.Redact(err.Error()))
	os.Exit(1)
}
