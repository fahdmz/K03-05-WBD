package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/fahdmz/K03-05-WBD/internal/admin"
	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/authz"
	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"
	"github.com/fahdmz/K03-05-WBD/internal/history"
	"github.com/fahdmz/K03-05-WBD/internal/migration"
	"github.com/fahdmz/K03-05-WBD/internal/obs"
	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

func main() {
	godotenv.Load()
	shutdown, err := obs.Setup("server")
	if err != nil {
		fatal("obs.setup_failed", err)
	}
	defer shutdown(context.Background())
	ctx := context.Background()

	db, err := store.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		fatal("server.db_connect_failed", err)
	}
	defer db.Close()

	authSvc := auth.Init(db)
	username, password := os.Getenv("ADMIN_USERNAME"), os.Getenv("ADMIN_PASSWORD")
	if username != "" && password != "" {
		if err := authSvc.EnsureAdmin(ctx, username, password); err != nil {
			fatal("server.seed_admin_failed", err)
		}
	}

	mux := http.NewServeMux()

	sessionFactory := func(tx pgx.Tx) admin.SessionManager {
		return authSvc.InTx(tx)
	}

	adminHandler := admin.NewHandler(db).WithSessionManagerFactory(sessionFactory)

	mux.Handle(
		"GET /api/admin/users",
		auth.RequireLogin(http.HandlerFunc(adminHandler.ListUsers)),
	)

	mux.Handle(
		"POST /api/admin/users",
		auth.RequireLogin(http.HandlerFunc(adminHandler.CreateUser)),
	)

	mux.Handle(
		"PATCH /api/admin/users/{id}",
		auth.RequireLogin(http.HandlerFunc(adminHandler.UpdateUser)),
	)

	mux.Handle(
		"DELETE /api/admin/users/{id}",
		auth.RequireLogin(http.HandlerFunc(adminHandler.DeleteUser)),
	)

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(r.Context()); err != nil {
			http.Error(w, "database unreachable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})

	authSvc.RegisterRoutes(mux)

	manager, err := dbmanager.NewPostgresManagerFromEnv(db)
	if err != nil {
		fatal("server.manager_failed", err)
	}
	authorizer := authz.NewPostgresAuthorizer(db)

	resourceHandler, err := admin.NewResourceHandler(
		db,
		manager,
		os.Getenv("DB_ENCRYPTION_KEY"),
	)
	if err != nil {
		fatal("server.resource_handler_failed", err)
	}
	resourceHandler = resourceHandler.WithSessionManagerFactory(sessionFactory)

	resourceHandler.RegisterRoutes(mux)

	// Viewer dan migration mendaftarkan route sendiri, jadi dipasang di sub-mux
	// lalu dibungkus RequireLogin sekali.
	databaseMux := http.NewServeMux()
	dbmanager.NewViewer(db, manager, authorizer).Register(databaseMux)
	migration.NewHandler(db, manager, authorizer).Register(databaseMux)
	history.NewHandler(db, authorizer).Register(databaseMux)
	databaseRoutes := auth.RequireLogin(databaseMux)
	mux.Handle("/api/databases", databaseRoutes)
	mux.Handle("/api/databases/", databaseRoutes)
	mux.Handle("/api/migrations/", databaseRoutes)
	mux.Handle("/api/jobs/", databaseRoutes)

	slog.Info("server.started", "addr", ":8080")
	if err := http.ListenAndServe(":8080", obs.HTTPMiddleware(mux)); err != nil {
		fatal("server.stopped", err)
	}
}

func fatal(event string, err error) {
	slog.Error(event, "error", obs.Redact(err.Error()))
	os.Exit(1)
}
