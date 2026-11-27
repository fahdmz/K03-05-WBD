package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/obs"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

const (
	defURL   = "postgres://target:target@localhost:5433/shop?sslmode=disable"
	defRows  = 3_000_000
	defChunk = 500_000
	minDur   = time.Second
)

type cfg struct {
	url         string
	rows        int64
	chunk       int64
	reset       bool
	measureOnly bool
}

type result struct {
	inserted int64
	total    int64
	updated  int64
	dur      time.Duration
}

func main() {
	godotenv.Load()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "seed-bigdata")

	c, err := parseArgs(os.Args[1:], os.Getenv)
	if err != nil {
		log.Error("seed.invalid_args", "error", err.Error())
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, err := run(ctx, c, log); err != nil {
		log.Error("seed.failed", "error", obs.Redact(err.Error()))
		os.Exit(1)
	}
}

func parseArgs(args []string, getenv func(string) string) (cfg, error) {
	c := cfg{url: getenv("TARGET_DATABASE_URL")}
	if c.url == "" {
		c.url = defURL
	}
	fs := flag.NewFlagSet("seed-bigdata", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&c.url, "url", c.url, "URL database target")
	fs.Int64Var(&c.rows, "rows", defRows, "jumlah row orders_big")
	fs.Int64Var(&c.chunk, "chunk", defChunk, "row per INSERT")
	fs.BoolVar(&c.reset, "reset", false, "drop & bikin ulang orders_big")
	fs.BoolVar(&c.measureOnly, "measure-only", false, "cuma ukur UPDATE tanpa batch")
	if err := fs.Parse(args); err != nil {
		return cfg{}, fmt.Errorf("flag tidak valid: %w", err)
	}
	if fs.NArg() > 0 {
		return cfg{}, fmt.Errorf("argumen tidak dikenal: %v", fs.Args())
	}
	if c.url == "" {
		return cfg{}, errors.New("URL database target kosong")
	}
	if c.rows < 1 {
		return cfg{}, errors.New("-rows minimal 1")
	}
	if c.chunk < 1 {
		return cfg{}, errors.New("-chunk minimal 1")
	}
	if c.reset && c.measureOnly {
		return cfg{}, errors.New("-reset dan -measure-only tidak bisa dipakai bareng")
	}
	return c, nil
}

func run(ctx context.Context, c cfg, log *slog.Logger) (result, error) {
	var res result
	conn, err := pgx.Connect(ctx, c.url)
	if err != nil {
		return res, fmt.Errorf("koneksi ke database target: %w", err)
	}
	defer conn.Close(context.Background())

	// seed & UPDATE 3 juta row bisa lama, jangan sampai kena timeout bawaan server
	if _, err := conn.Exec(ctx, "SET statement_timeout = 0"); err != nil {
		return res, fmt.Errorf("set statement_timeout: %w", err)
	}

	if !c.measureOnly {
		res.inserted, err = seed(ctx, conn, c, log)
		if err != nil {
			return res, err
		}
	}

	res.total, res.updated, res.dur, err = measure(ctx, conn, log)
	if err != nil {
		return res, err
	}
	log.Info("seed.measured",
		"rows", res.updated,
		"duration_ms", res.dur.Milliseconds(),
		"duration", res.dur.Round(time.Millisecond).String(),
	)
	if res.dur <= minDur {
		log.Warn("seed.too_fast",
			"duration_ms", res.dur.Milliseconds(),
			"hint", "UPDATE tanpa batch harus > 1 detik (spek), naikkan -rows lalu jalankan lagi dengan -reset",
		)
	}
	return res, nil
}

func seed(ctx context.Context, conn *pgx.Conn, c cfg, log *slog.Logger) (int64, error) {
	if c.reset {
		if _, err := conn.Exec(ctx, "DROP TABLE IF EXISTS orders_big"); err != nil {
			return 0, fmt.Errorf("drop orders_big: %w", err)
		}
		log.Info("seed.reset")
	}
	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS orders_big (
			id BIGSERIAL PRIMARY KEY,
			customer_id INT NOT NULL,
			price NUMERIC(12,2) NOT NULL,
			status TEXT NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return 0, fmt.Errorf("create orders_big: %w", err)
	}

	var cur int64
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM orders_big").Scan(&cur); err != nil {
		return 0, fmt.Errorf("hitung orders_big: %w", err)
	}
	if cur >= c.rows {
		log.Info("seed.skipped", "rows", cur, "reason", "tabel sudah berisi cukup row")
		return 0, nil
	}

	start := time.Now()
	var done int64
	for lo := cur + 1; lo <= c.rows; lo += c.chunk {
		hi := min(lo+c.chunk-1, c.rows)
		// data deterministik dari g, jadi tiap seed hasilnya sama
		tag, err := conn.Exec(ctx, `
			INSERT INTO orders_big (customer_id, price, status, updated_at)
			SELECT
				(g % 10000) + 1,
				((g % 100000) + 100) / 100.0,
				(ARRAY['new', 'paid', 'shipped', 'done'])[(g % 4) + 1],
				TIMESTAMPTZ '2026-01-01 00:00:00+00' + (g % 525600) * INTERVAL '1 minute'
			FROM generate_series($1::bigint, $2::bigint) AS g`, lo, hi)
		if err != nil {
			return done, fmt.Errorf("insert row %d-%d: %w", lo, hi, err)
		}
		done += tag.RowsAffected()
		log.Info("seed.chunk_inserted", "from", lo, "to", hi, "inserted", done)
	}

	// VACUUM ANALYZE: statistik + hint bit beres, jadi pengukuran nggak ikut nanggung kerja itu
	if _, err := conn.Exec(ctx, "VACUUM ANALYZE orders_big"); err != nil {
		return done, fmt.Errorf("vacuum analyze orders_big: %w", err)
	}
	log.Info("seed.inserted", "inserted", done, "duration_ms", time.Since(start).Milliseconds())
	return done, nil
}

func measure(ctx context.Context, conn *pgx.Conn, log *slog.Logger) (total, updated int64, dur time.Duration, err error) {
	var exists bool
	if err = conn.QueryRow(ctx, `SELECT to_regclass('orders_big') IS NOT NULL`).Scan(&exists); err != nil {
		return 0, 0, 0, fmt.Errorf("cek orders_big: %w", err)
	}
	if !exists {
		return 0, 0, 0, errors.New("tabel orders_big belum ada, jalankan tanpa -measure-only dulu")
	}
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM orders_big").Scan(&total); err != nil {
		return 0, 0, 0, fmt.Errorf("hitung orders_big: %w", err)
	}
	log.Info("seed.measure_started", "rows", total)

	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("begin: %w", err)
	}
	// selalu ROLLBACK, dataset harus tetap sama buat uji batch
	defer tx.Rollback(context.Background())

	start := time.Now()
	tag, err := tx.Exec(ctx, "UPDATE orders_big SET price = price * 1.1")
	dur = time.Since(start)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("update tanpa batch: %w", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		return 0, 0, 0, fmt.Errorf("rollback: %w", err)
	}

	// buang dead tuple sisa UPDATE yang di-rollback
	if _, err = conn.Exec(ctx, "VACUUM orders_big"); err != nil {
		return 0, 0, 0, fmt.Errorf("vacuum orders_big: %w", err)
	}
	return total, tag.RowsAffected(), dur, nil
}
