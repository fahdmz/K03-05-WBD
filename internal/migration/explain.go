package migration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/authz"
	"github.com/fahdmz/K03-05-WBD/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (h *Handler) explain(w http.ResponseWriter, r *http.Request) {
	dbID, ok := pathID(w, r, "id")
	if !ok {
		return
	}

	if _, ok := h.requireAccess(w, r, dbID, authz.Edit); !ok {
		return
	}
	if !h.databaseExists(w, r.Context(), dbID) {
		return
	}
	if h.manager == nil {
		web.WriteError(w, 500, "Database manager belum dikonfigurasi")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var input struct {
		SQL string `json:"sql"`
	}
	if err := decoder.Decode(&input); err != nil {
		web.WriteError(w, 400, "Request harus berupa JSON dengan field sql")
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		web.WriteError(w, 400, "Request hanya boleh berisi satu objek JSON")
		return
	}

	input.SQL = strings.TrimSpace(input.SQL)
	if input.SQL == "" {
		web.WriteError(w, 400, "SQL wajib diisi")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	pool, err := h.manager.Pool(ctx, dbID)
	if err != nil {
		web.WriteError(w, 502, "Koneksi ke database target gagal")
		return
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		web.WriteError(w, 502, "Gagal memulai transaksi pada database target")
		return
	}

	// Cleanup tetap bisa berjalan ketika context request sudah timeout.
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(), 2*time.Second,
		)
		defer cleanupCancel()
		_ = tx.Rollback(cleanupCtx)
	}()

	for _, setting := range []string{
		"SET LOCAL statement_timeout = '5s'",
		"SET LOCAL lock_timeout = '2s'",
	} {
		if _, err := tx.Exec(ctx, setting); err != nil {
			web.WriteError(w, 502, "Gagal menyiapkan transaksi EXPLAIN")
			return
		}
	}

	var plan json.RawMessage
	err = tx.QueryRow(
		ctx,
		"EXPLAIN (FORMAT JSON, ANALYZE FALSE) "+input.SQL,
		// Extended protocol menolak input dengan beberapa statement.
		pgx.QueryExecModeDescribeExec,
	).Scan(&plan)

	if err != nil {
		writeExplainError(w, ctx, err, input.SQL)
		return
	}
	if !json.Valid(plan) {
		web.WriteError(w, 500, "Format execution plan tidak valid")
		return
	}

	web.WriteJSON(w, http.StatusOK, struct {
		DatabaseID int64           `json:"databaseId"`
		Plan       json.RawMessage `json:"plan"`
	}{
		DatabaseID: dbID,
		Plan:       plan,
	})
}

func writeExplainError(
	w http.ResponseWriter,
	ctx context.Context,
	err error,
	sql ...string,
) {
	var pgErr *pgconn.PgError

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		web.WriteError(w, 504, "Analisis query melewati batas waktu")

	case errors.As(err, &pgErr):
		switch pgErr.Code {
		case "57014", "55P03":
			web.WriteError(w, 504, "Analisis terlalu lama atau menunggu lock")
		case "42601":
			message := "Sintaks SQL tidak valid. Periksa penulisan query."
			if pgErr.Message == "cannot insert multiple commands into a prepared statement" {
				message = "Hanya satu statement SQL yang boleh dianalisis. Jalankan EXPLAIN untuk setiap statement secara terpisah."
			} else if len(sql) > 0 {
				words := strings.Fields(sql[0])
				if len(words) > 0 {
					switch strings.ToUpper(words[0]) {
					case "CREATE", "ALTER", "DROP", "TRUNCATE", "EXPLAIN":
						message = "Statement ini tidak dapat dianalisis. Gunakan SELECT, INSERT, UPDATE, atau DELETE yang valid tanpa awalan EXPLAIN."
					}
				}
			}
			web.WriteError(w, 400, message)
		case "0A000":
			web.WriteError(w, 400, "Fitur SQL ini tidak didukung oleh EXPLAIN pada database target.")
		case "42P01":
			web.WriteError(w, 400, "Tabel atau relasi tidak ditemukan. Periksa nama schema dan tabel.")
		case "42703":
			web.WriteError(w, 400, "Kolom tidak ditemukan. Periksa nama kolom pada query.")
		case "25006":
			web.WriteError(w, 400, "Operasi ditolak dalam transaksi read-only")
		case "42501":
			web.WriteError(w, 403, "Akun database target tidak memiliki izin")
		default:
			if strings.HasPrefix(pgErr.Code, "42") ||
				strings.HasPrefix(pgErr.Code, "22") {
				web.WriteError(w, 400,
					"SQL tidak valid; periksa tabel, kolom, dan tipe data")
				return
			}
			web.WriteError(w, 502, "Database target gagal membuat execution plan")
		}

	default:
		web.WriteError(w, 502, "Gagal membaca execution plan")
	}
}
