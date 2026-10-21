package main

// Testing
import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/joho/godotenv"
)

func main() {
	godotenv.Load()
	ctx := context.Background()
	db, err := store.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("connect db : %v", err)
	}
	defer db.Close()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(r.Context()); err != nil {
			http.Error(w, "database unreachable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})

	log.Println("listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
