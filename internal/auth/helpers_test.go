package auth

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const testSchema = "auth_test"

func testDatabaseURL() string {
	if u := os.Getenv("TEST_DATABASE_URL"); u != "" {
		return u
	}
	return "postgres://test:test@localhost:5434/test?sslmode=disable"
}

func setupDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, testDatabaseURL())
	if err != nil {
		t.Fatalf("koneksi test-db: %v", err)
	}
	defer admin.Close()
	if err := admin.Ping(ctx); err != nil {
		t.Skipf("test-db tidak bisa dihubungi, jalankan docker compose up -d: %v", err)
	}
	_, err = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+testSchema+" CASCADE; CREATE SCHEMA "+testSchema)
	if err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	// Didaftarkan sebelum pool.Close, jadi berjalan setelah pool test ditutup.
	t.Cleanup(func() {
		cleanup, err := pgxpool.New(context.Background(), testDatabaseURL())
		if err != nil {
			t.Errorf("koneksi cleanup: %v", err)
			return
		}
		defer cleanup.Close()
		if _, err := cleanup.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+testSchema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", testSchema, err)
		}
	})

	cfg, err := pgxpool.ParseConfig(testDatabaseURL())
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = testSchema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("koneksi pool: %v", err)
	}
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

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestService(t *testing.T) (*Service, *fakeClock) {
	t.Helper()
	pool := setupDB(t)
	clock := &fakeClock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	s := NewService(pool, DefaultConfig())
	s.now = clock.Now
	return s, clock
}

func createUser(t *testing.T, s *Service, username, password string, active bool) int64 {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	err = s.pool.QueryRow(context.Background(),
		`INSERT INTO users (username, password_hash, is_active) VALUES ($1, $2, $3) RETURNING id`,
		username, hash, active,
	).Scan(&id)
	if err != nil {
		t.Fatalf("buat user: %v", err)
	}
	return id
}

func mustLogin(t *testing.T, s *Service, username, password string) string {
	t.Helper()
	token, _, err := s.Login(context.Background(), username, password)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return token
}

func touch(s *Service, token string) (string, error) {
	sess, err := s.Authenticate(context.Background(), token)
	if err != nil {
		return token, err
	}
	if sess.NewToken != "" {
		return sess.NewToken, nil
	}
	return token, nil
}
