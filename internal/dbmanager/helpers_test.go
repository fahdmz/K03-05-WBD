package dbmanager

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testSchema = "dbmanager_test"

func testDatabaseURL() string {
	if u := os.Getenv("TEST_DATABASE_URL"); u != "" {
		return u
	}
	return "postgres://test:test@localhost:5434/test?sslmode=disable"
}

// envOr mengembalikan nilai environment variable lama sebagai override, atau
// fallback bila tidak diset.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func connectOrSkip(t *testing.T, cfg *pgxpool.Config) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("koneksi test-db: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("test-db tidak bisa dihubungi, jalankan docker compose up -d test-db: %v", err)
	}
	return pool
}

// setupPlatformDB membuat ulang schema testSchema, menerapkan
// migrations/001_init.sql ke dalamnya, dan mengembalikan pool dengan
// search_path ke schema itu. Schema di-drop lagi setelah test selesai.
func setupPlatformDB(t *testing.T, databaseURL string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	adminCfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	admin := connectOrSkip(t, adminCfg)
	t.Cleanup(func() {
		defer admin.Close()
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+testSchema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", testSchema, err)
		}
	})
	if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+testSchema+" CASCADE; CREATE SCHEMA "+testSchema); err != nil {
		t.Fatalf("reset schema: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = testSchema
	pool := connectOrSkip(t, cfg)
	t.Cleanup(pool.Close)

	schema, err := os.ReadFile("../../migrations/001_init.sql")
	if err != nil {
		t.Fatalf("baca schema: %v", err)
	}
	if _, err := pool.Exec(ctx, string(schema)); err != nil {
		t.Fatalf("buat tabel: %v", err)
	}
	return pool
}

// setupTargetDB membuka pool ke database target dan mengembalikan schema
// tempat tabel tanpa kualifikasi akan dibuat. Pool ditutup lewat t.Cleanup,
// sehingga cleanup yang didaftarkan setelahnya tetap bisa memakai pool.
func setupTargetDB(t *testing.T, databaseURL string) (*pgxpool.Pool, string) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse target config: %v", err)
	}
	pool := connectOrSkip(t, cfg)
	t.Cleanup(pool.Close)

	var schema *string
	if err := pool.QueryRow(context.Background(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatalf("baca current_schema target: %v", err)
	}
	if schema == nil {
		t.Fatal("database target tidak punya schema aktif di search_path")
	}
	return pool, *schema
}

// dropTargetTable menghapus tabel buatan test di database target dan
// melaporkan kegagalan lewat t.Errorf.
func dropTargetTable(t *testing.T, pool *pgxpool.Pool, schema, table string) {
	t.Helper()
	name := pgx.Identifier{schema, table}.Sanitize()
	if _, err := pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+name); err != nil {
		t.Errorf("drop table %s: %v", name, err)
	}
}
