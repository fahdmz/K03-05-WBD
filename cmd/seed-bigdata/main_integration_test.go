package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// tempDB bikin database acak di test-db, di-drop lagi pas cleanup.
func tempDB(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		base = "postgres://test:test@localhost:5434/test?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, base)
	if err == nil {
		err = admin.Ping(ctx)
	}
	if err != nil {
		t.Skipf("test-db tidak bisa dihubungi, jalankan docker compose up -d test-db: %v", err)
	}

	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	name := "seedbig_" + hex.EncodeToString(b)
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		admin.Close(context.Background())
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		defer admin.Close(ctx)
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func TestSeedBigdataIntegration(t *testing.T) {
	dbURL := tempDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))

	if _, err := run(ctx, cfg{url: dbURL, rows: 10, chunk: 5, measureOnly: true}, log); err == nil ||
		!strings.Contains(err.Error(), "belum ada") {
		t.Fatalf("measure-only tanpa tabel: err = %v", err)
	}

	res, err := run(ctx, cfg{url: dbURL, rows: 2500, chunk: 1000}, log)
	if err != nil {
		t.Fatal(err)
	}
	if res.inserted != 2500 || res.total != 2500 || res.updated != 2500 {
		t.Fatalf("res = %+v, want 2500 semua", res)
	}
	if strings.Count(logs.String(), `"seed.chunk_inserted"`) != 3 {
		t.Fatalf("harusnya 3 chunk insert, log:\n%s", logs.String())
	}
	// dataset kecil pasti < 1 detik, peringatan harus muncul
	if !strings.Contains(logs.String(), `"seed.too_fast"`) {
		t.Fatalf("peringatan durasi nggak muncul, log:\n%s", logs.String())
	}

	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())

	var sum, minID, maxID string
	if err := conn.QueryRow(ctx, `SELECT sum(price)::text, min(id)::text, max(id)::text FROM orders_big`).
		Scan(&sum, &minID, &maxID); err != nil {
		t.Fatal(err)
	}
	if minID != "1" || maxID != "2500" {
		t.Fatalf("id = %s..%s, want 1..2500", minID, maxID)
	}

	// measure-only: UPDATE di-rollback, harga nggak berubah
	res, err = run(ctx, cfg{url: dbURL, rows: defRows, chunk: defChunk, measureOnly: true}, log)
	if err != nil {
		t.Fatal(err)
	}
	if res.inserted != 0 || res.updated != 2500 {
		t.Fatalf("measure-only res = %+v", res)
	}
	var after string
	if err := conn.QueryRow(ctx, `SELECT sum(price)::text FROM orders_big`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != sum {
		t.Fatalf("sum(price) = %s, want %s (UPDATE harus di-rollback)", after, sum)
	}

	// tanpa reset: cuma nambah kekurangannya
	res, err = run(ctx, cfg{url: dbURL, rows: 3000, chunk: 1000}, log)
	if err != nil {
		t.Fatal(err)
	}
	if res.inserted != 500 || res.total != 3000 {
		t.Fatalf("top-up res = %+v, want insert 500 total 3000", res)
	}

	res, err = run(ctx, cfg{url: dbURL, rows: 100, chunk: 1000, reset: true}, log)
	if err != nil {
		t.Fatal(err)
	}
	if res.inserted != 100 || res.total != 100 {
		t.Fatalf("reset res = %+v", res)
	}
	if err := conn.QueryRow(ctx, `SELECT min(id)::text FROM orders_big`).Scan(&minID); err != nil {
		t.Fatal(err)
	}
	if minID != "1" {
		t.Fatalf("setelah reset id mulai dari %s, want 1", minID)
	}
}
