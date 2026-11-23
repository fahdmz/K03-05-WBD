package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"
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

	w := worker.New(db, manager)
	slog.Info("worker.started", "worker_id", w.ID())
	w.Run(ctx)
	slog.Info("worker.stopped", "worker_id", w.ID())
}

func fatal(event string, err error) {
	slog.Error(event, "error", obs.Redact(err.Error()))
	os.Exit(1)
}
