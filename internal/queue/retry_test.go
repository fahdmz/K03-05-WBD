package queue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsTransient(t *testing.T) {
	pg := func(code string) error { return fmt.Errorf("exec: %w", &pgconn.PgError{Code: code}) }
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"canceled", context.Canceled, false},
		{"biasa", errors.New("x"), false},
		{"serialization", pg("40001"), true},
		{"deadlock", pg("40P01"), true},
		{"lock not available", pg("55P03"), true},
		{"admin shutdown", pg("57P01"), true},
		{"statement timeout", pg("57014"), true},
		{"koneksi", pg("08006"), true},
		{"syntax", pg("42601"), false},
		{"tabel nggak ada", pg("42P01"), false},
		{"unique", pg("23505"), false},
		{"eof", fmt.Errorf("baca: %w", io.ErrUnexpectedEOF), true},
		{"deadline", context.DeadlineExceeded, true},
	}
	for _, c := range cases {
		if got := IsTransient(c.err); got != c.want {
			t.Errorf("%s: IsTransient = %v, mau %v", c.name, got, c.want)
		}
	}
}

func TestSQLState(t *testing.T) {
	if got := SQLState(fmt.Errorf("x: %w", &pgconn.PgError{Code: "40001"})); got != "40001" {
		t.Fatalf("SQLState = %q", got)
	}
	if got := SQLState(errors.New("x")); got != "" {
		t.Fatalf("SQLState error biasa = %q", got)
	}
}

func TestBackoff(t *testing.T) {
	want := map[int]time.Duration{0: 2 * time.Second, 1: 2 * time.Second, 2: 4 * time.Second,
		3: 8 * time.Second, 4: 16 * time.Second, 5: 16 * time.Second, 9: 16 * time.Second}
	for a, d := range want {
		if got := Backoff(a); got != d {
			t.Errorf("Backoff(%d) = %v, mau %v", a, got, d)
		}
	}
}
