package queue

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// SQLState balikin kode SQLSTATE kalau error-nya dari Postgres
func SQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// IsTransient: error sementara yang layak di-retry
func IsTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		// gagal konek = sementara, kecuali login salah / database nggak ada
		code := SQLState(err)
		return !(len(code) == 5 && code[:2] == "28") && code != "3D000"
	}
	switch code := SQLState(err); {
	case code == "":
	case len(code) == 5 && (code[:2] == "08" || code[:2] == "53"):
		return true
	case code == "57P03":
		return true
	case code == "40001", code == "40P01", code == "55P03", code == "57P01", code == "57014":
		return true
	default:
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		pgconn.SafeToRetry(err) || pgconn.Timeout(err) || errors.Is(err, context.DeadlineExceeded)
}

// Backoff: jeda sebelum attempt berikutnya (2, 4, 8, 16 detik)
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 4 {
		attempt = 4
	}
	return time.Duration(1<<attempt) * time.Second
}
