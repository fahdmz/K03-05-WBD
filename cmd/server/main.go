package main

// Testing
import (
	"context" // tool for giving stop signals
	"fmt"
	"log"      //tool for printing messages in terminal
	"net/http" // tool to help build web server
	"os"       // tool for talking to a computer

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/fahdmz/K03-05-WBD/internal/admin"
	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"
	"github.com/joho/godotenv"
)

func main() {
	godotenv.Load() // loads the env
	ctx := context.Background()
	db, err := store.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("connect db : %v", err)
	}
	defer db.Close()

	mux := http.NewServeMux()
	
	adminHandler := admin.NewHandler(db)

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
	mux.Handle("GET /api/me", auth.RequireLogin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := auth.UserFromContext(r.Context())
		fmt.Fprintf(w, "hello %s, admin=%v", u.Username, u.IsAdmin)
	})))

	resourceHandler, err := admin.NewResourceHandler(
		db,
		dbmanager.Stub{},
		os.Getenv("DB_ENCRYPTION_KEY"),
	)
	if err != nil {
		log.Fatal(err)
	}

	resourceHandler.RegisterRoutes(mux)

	log.Println("listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
