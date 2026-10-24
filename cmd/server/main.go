package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/fahdmz/K03-05-WBD/internal/admin"
	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

func main() {
	godotenv.Load()
	ctx := context.Background()

	db, err := store.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer db.Close()

	authSvc := auth.Init(db)
	username, password := os.Getenv("ADMIN_USERNAME"), os.Getenv("ADMIN_PASSWORD")
	if username != "" && password != "" {
		if err := authSvc.EnsureAdmin(ctx, username, password); err != nil {
			log.Fatalf("seed admin: %v", err)
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

	resourceHandler, err := admin.NewResourceHandler(
		db,
		dbmanager.Stub{},
		os.Getenv("DB_ENCRYPTION_KEY"),
	)
	if err != nil {
		log.Fatal(err)
	}
	resourceHandler = resourceHandler.WithSessionManagerFactory(sessionFactory)

	resourceHandler.RegisterRoutes(mux)

	log.Println("listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
