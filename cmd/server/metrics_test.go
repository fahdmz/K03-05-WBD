package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMetricsHandler(t *testing.T) {
	db, err := pgxpool.New(context.Background(), "postgres://x:x@127.0.0.1:1/x")
	if err != nil {
		t.Fatalf("buat pool: %v", err)
	}
	defer db.Close()

	rec := httptest.NewRecorder()
	newMetrics(db).Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	out := rec.Body.String()
	for _, want := range []string{
		"# TYPE server_db_pool_connections gauge",
		`server_db_pool_connections{state="active"} 0`,
		`server_db_pool_connections{state="idle"}`,
		`server_db_pool_connections{state="total"}`,
		"# TYPE server_db_pool_acquire_wait_seconds_total counter",
		"server_db_pool_acquire_wait_seconds_total ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output nggak ada %q\n%s", want, out)
		}
	}
}
