package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

func TestSeedAdminIntegration(t *testing.T) {
	testURL := testDatabaseURL(t)

	schemaSQL, err := os.ReadFile(
		filepath.Join("..", "..", "migrations", "001_init.sql"),
	)
	if err != nil {
		t.Fatal(err)
	}

	parsedURL, err := url.Parse(testURL)
	if err != nil {
		t.Fatal("TEST_DATABASE_URL tidak valid")
	}
	if parsedURL.Scheme != "postgres" &&
		parsedURL.Scheme != "postgresql" {
		t.Fatal("TEST_DATABASE_URL harus berupa URL PostgreSQL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	control, err := pgxpool.New(ctx, testURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.Close)

	randomBytes := make([]byte, 12)
	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatal(err)
	}

	schemaName := "seed_test_" + hex.EncodeToString(randomBytes)
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()

	if _, err := control.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(), 10*time.Second,
		)
		defer cleanupCancel()

		if _, err := control.Exec(
			cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE",
		); err != nil {
			t.Errorf("cleanup schema: %v", err)
		}
	})

	params := parsedURL.Query()
	params.Set("search_path", schemaName)
	parsedURL.RawQuery = params.Encode()
	scopedURL := parsedURL.String()

	db, err := pgxpool.New(ctx, scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	if _, err := db.Exec(ctx, string(schemaSQL)); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	t.Setenv("DATABASE_URL", scopedURL)
	t.Setenv("ADMIN_USERNAME", "  first-admin  ")
	t.Setenv("ADMIN_PASSWORD", "first-password123")

	if err := run(); err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	var id int64
	var username, originalHash string
	var isAdmin, isActive bool

	err = db.QueryRow(ctx, `
		SELECT id, username, password_hash, is_admin, is_active
		FROM users
		WHERE username = 'first-admin'
	`).Scan(&id, &username, &originalHash, &isAdmin, &isActive)
	if err != nil {
		t.Fatal(err)
	}

	if username != "first-admin" || !isAdmin || !isActive {
		t.Fatalf(
			"unexpected account: username=%s admin=%v active=%v",
			username, isAdmin, isActive,
		)
	}

	if originalHash == "first-password123" {
		t.Fatal("password stored as plaintext")
	}
	if err := bcrypt.CompareHashAndPassword(
		[]byte(originalHash), []byte("first-password123"),
	); err != nil {
		t.Fatalf("stored hash does not match password: %v", err)
	}

	// A duplicate seed must not reactivate or promote an existing account.
	if _, err := db.Exec(ctx, `
		UPDATE users
		SET is_admin = FALSE, is_active = FALSE
		WHERE id = $1
	`, id); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ADMIN_PASSWORD", "replacement-password123")

	err = run()
	if err == nil || !strings.Contains(err.Error(), "username sudah digunakan") {
		t.Fatalf("expected duplicate username error, got %v", err)
	}

	var currentHash string
	err = db.QueryRow(ctx, `
		SELECT password_hash, is_admin, is_active
		FROM users WHERE id = $1
	`, id).Scan(&currentHash, &isAdmin, &isActive)
	if err != nil {
		t.Fatal(err)
	}

	if currentHash != originalHash || isAdmin || isActive {
		t.Fatal("duplicate seed changed the existing account")
	}

	var count int
	if err := db.QueryRow(ctx,
		"SELECT COUNT(*) FROM users",
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("user count = %d, want 1", count)
	}

	if _, err := db.Exec(ctx, "DROP TABLE users CASCADE"); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ADMIN_USERNAME", "another-admin")
	err = run()
	if err == nil ||
		err.Error() != "gagal membuat admin; periksa schema dan koneksi database" {
		t.Fatalf("expected generic database error, got %v", err)
	}
}

// testDatabaseURL mengembalikan TEST_DATABASE_URL (atau default test-db) dan
// men-skip test kalau database itu tidak bisa dihubungi.
func testDatabaseURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		u = "postgres://test:test@localhost:5434/test?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, u)
	if err == nil {
		err = conn.Ping(ctx)
		_ = conn.Close(ctx)
	}
	if err != nil {
		t.Skipf("test-db tidak bisa dihubungi, jalankan docker compose up -d test-db: %v", err)
	}
	return u
}
