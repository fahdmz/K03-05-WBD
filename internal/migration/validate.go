package migration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	maxBatchSize      = 100000
	dryRunTimeout     = 12 * time.Second
	rollbackGraceTime = 2 * time.Second
)

type ValidationResult struct {
	Valid    bool     `jason:"valid"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

type RevisionSpec struct {
	Mode               string
	SQLUp              string
	SQLDown            *string
	Irreversible       bool
	IrreversibleReason *string
	BatchTable         *string
	BatchColumn        *string
	BatchSize          *int
}

func ValidateStructure(spec RevisionSpec) ValidationResult {
	res := ValidationResult{
		Errors:   make([]string, 0),
		Warnings: make([]string, 0),
	}
	if strings.TrimSpace(spec.SQLUp) == "" {
		res.Errors = append(res.Errors, "SQL up wajib diisi")
	}
	if spec.Irreversible {
		if spec.IrreversibleReason == nil || strings.TrimSpace(*spec.IrreversibleReason) == "" {
			res.Errors = append(res.Errors, "Alasan irreversible wajib diisi")
		}
	} else {
		if spec.SQLDown == nil || strings.TrimSpace(*spec.SQLDown) == "" {
			res.Errors = append(res.Errors, "SQL down wajib diisi kecuali ditandai irreversible")
		}
	}
	switch spec.Mode {
	case "normal":
	case "batch":
		if spec.BatchTable == nil || strings.TrimSpace(*spec.BatchTable) == "" {
			res.Errors = append(res.Errors, "Nama tabel batch wajib diisi")
		}
		if spec.BatchColumn == nil || strings.TrimSpace(*spec.BatchColumn) == "" {
			res.Errors = append(res.Errors, "Nama kolom batch wajib diisi")
		}
		if spec.BatchSize == nil || *spec.BatchSize < 1 || *spec.BatchSize > maxBatchSize {
			res.Errors = append(res.Errors, fmt.Sprintf("Batch size harus antara 1 dan %d", maxBatchSize))
		}
		if !hasBatchPlaceholders(spec.SQLUp) {
			res.Errors = append(res.Errors, "SQL up mode batch wajib memuat placeholder {{lo}} dan {{hi}}")

		}
		if !spec.Irreversible && spec.SQLDown != nil && !hasBatchPlaceholders(*spec.SQLDown) {
			res.Errors = append(res.Errors, "SQL down mode batch wajib memuat placeholder {{lo}} dan {{hi}}")
		}
	default:
		res.Errors = append(res.Errors, "Mode migration harus normal atau batch")
	}
	upStmts := splitSQLStatements(spec.SQLUp)
	if kw := findForbiddenTransactionStmt(upStmts); kw != "" {
		res.Errors = append(res.Errors, fmt.Sprintf("SQL up tidak boleh mengandung perintah transaksi (%s)", kw))
	}
	if spec.Mode == "batch" && len(upStmts) > 1 {
		res.Errors = append(res.Errors, "SQL up mode batch hanya boleh berisi satu statement")
	}
	if !spec.Irreversible && spec.SQLDown != nil && strings.TrimSpace(*spec.SQLDown) != "" {
		downStmts := splitSQLStatements(*spec.SQLDown)
		if kw := findForbiddenTransactionStmt(downStmts); kw != "" {
			res.Errors = append(res.Errors, fmt.Sprintf("SQL down tidak boleh mengandung perintah transaksi (%s)", kw))
		}
		if spec.Mode == "batch" && len(downStmts) > 1 {
			res.Errors = append(res.Errors, "SQL down mode batch hanya boleh berisi satu statement")
		}
	}
	res.Valid = len(res.Errors) == 0
	return res
}
func hasBatchPlaceholders(sql string) bool {
	return strings.Contains(sql, "{{lo}}") && strings.Contains(sql, "{{hi}}")
}

func findForbiddenTransactionStmt(stmts []string) string {
	for _, stmt := range stmts {
		words := strings.Fields(stmt)
		if len(words) == 0 {
			continue
		}
		first := strings.ToUpper(words[0])
		switch first {
		case "BEGIN", "COMMIT", "END", "ROLLBACK", "SAVEPOINT", "RELEASE":
			return first
		case "START", "PREPARE":
			if len(words) >= 2 && strings.ToUpper(words[1]) == "TRANSACTION" {
				return first + " TRANSACTION"
			}
		}
	}
	return ""
}

// splitSQLStatements memecah SQL menjadi daftar statement non-kosong,
// sambil mengabaikan komentar dan string literal (termasuk $$...$$).
func splitSQLStatements(sql string) []string {
	var stmts []string
	var cur strings.Builder
	n := len(sql)

	for i := 0; i < n; {
		c := sql[i]

		// Komentar baris: -- ...
		if c == '-' && i+1 < n && sql[i+1] == '-' {
			i += 2
			for i < n && sql[i] != '\n' {
				i++
			}
			cur.WriteByte(' ')
			continue
		}

		// Komentar blok: /* ... */
		if c == '/' && i+1 < n && sql[i+1] == '*' {
			i += 2
			for i+1 < n && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			if i+1 < n {
				i += 2
			}
			cur.WriteByte(' ')
			continue
		}

		// Single / double quoted literal
		if c == '\'' || c == '"' {
			quote := c
			cur.WriteByte(c)
			i++
			for i < n {
				cur.WriteByte(sql[i])
				if sql[i] == quote {
					if i+1 < n && sql[i+1] == quote {
						i++
						cur.WriteByte(sql[i])
					} else {
						i++
						break
					}
				}
				i++
			}
			continue
		}

		// Dollar-quoted literal: $$...$$ atau $tag$...$tag$
		// Tidak boleh diawali karakter identifier Postgres (huruf, angka, _, $, non-ASCII).
		if c == '$' && (i == 0 || !isSQLIdentChar(sql[i-1])) {
			if tag, ok := matchDollarTag(sql[i:]); ok {
				cur.WriteString(tag)
				i += len(tag)
				end := strings.Index(sql[i:], tag)
				if end == -1 {
					cur.WriteString(sql[i:])
					break
				}
				cur.WriteString(sql[i : i+end+len(tag)])
				i += end + len(tag)
				continue
			}
		}

		if c == ';' {
			if s := strings.TrimSpace(cur.String()); s != "" {
				stmts = append(stmts, s)
			}
			cur.Reset()
			i++
			continue
		}

		cur.WriteByte(c)
		i++
	}

	if s := strings.TrimSpace(cur.String()); s != "" {
		stmts = append(stmts, s)
	}
	return stmts
}

func isSQLIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '_' ||
		b == '$' ||
		b >= 0x80
}

func matchDollarTag(s string) (string, bool) {
	if len(s) < 2 || s[0] != '$' {
		return "", false
	}
	for i := 1; i < len(s); i++ {
		ch := s[i]
		if ch == '$' {
			return s[:i+1], true
		}
		isAlpha := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
		isDigit := ch >= '0' && ch <= '9'
		if !(isAlpha || (i > 1 && isDigit)) {
			return "", false
		}
	}
	return "", false
}

func ValidateRevision(ctx context.Context, targetPool *pgxpool.Pool, spec RevisionSpec) (ValidationResult, error) {
	res := ValidateStructure(spec)
	if !res.Valid {
		return res, nil
	}
	if targetPool == nil {
		return res, errors.New("target pool belum tersedia")
	}
	if spec.Mode == "batch" {
		if err := ValidateBatchTarget(ctx, targetPool, *spec.BatchTable, *spec.BatchColumn, &res); err != nil {
			return ValidationResult{}, err
		}
		res.Valid = len(res.Errors) == 0
		return res, nil
	}
	if err := runDryRun(ctx, targetPool, spec, &res); err != nil {
		return ValidationResult{}, err
	}
	res.Valid = len(res.Errors) == 0
	return res, nil
}

func splitSchemaTable(raw string) (schema, table string) {
	raw = strings.TrimSpace(raw)
	if parts := strings.SplitN(raw, ".", 2); len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return "", raw
}

func ValidateBatchTarget(
	ctx context.Context,
	pool *pgxpool.Pool,
	rawTable string,
	rawColumn string,
	res *ValidationResult,
) error {
	schema, table := splitSchemaTable(rawTable)
	column := strings.TrimSpace(rawColumn)
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if schema == "" {
		if err := pool.QueryRow(checkCtx, `SELECT current_schema()`).Scan(&schema); err != nil {
			return fmt.Errorf("baca current_schema target: %w", err)
		}
	}
	var attnum int16
	err := pool.QueryRow(checkCtx, `
		SELECT a.attnum
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid
		WHERE n.nspname = $1
		  AND c.relname = $2
		  AND c.relkind IN ('r', 'p')
		  AND a.attname = $3
		  AND a.attnum > 0
		  AND NOT a.attisdropped
	`, schema, table, column).Scan(&attnum)
	if errors.Is(err, pgx.ErrNoRows) {
		res.Errors = append(res.Errors, fmt.Sprintf("Tabel %s.%s atau kolom %s tidak ditemukan", schema, table, column))
		return nil
	}
	if err != nil {
		return fmt.Errorf("cek tabel dan kolom batch: %w", err)
	}
	var hasUniqueIndex bool
	err = pool.QueryRow(checkCtx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_catalog.pg_index i
			JOIN pg_catalog.pg_class c ON c.oid = i.indrelid
			JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1
			  AND c.relname = $2
			  AND i.indisunique = TRUE
			  AND i.indisvalid = TRUE
			  AND i.indpred IS NULL
			  AND i.indnkeyatts = 1
			  AND i.indkey[0] = $3
		)
	`, schema, table, attnum).Scan(&hasUniqueIndex)
	if err != nil {
		return fmt.Errorf("cek unique index batch: %w", err)
	}
	if !hasUniqueIndex {
		res.Errors = append(res.Errors, fmt.Sprintf("Kolom %s pada %s.%s wajib memiliki unique index", column, schema, table))
	}
	return nil
}

func runDryRun(
	ctx context.Context,
	pool *pgxpool.Pool,
	spec RevisionSpec,
	res *ValidationResult,
) error {
	runCtx, cancel := context.WithTimeout(ctx, dryRunTimeout)
	defer cancel()
	tx, err := pool.Begin(runCtx)
	if err != nil {
		return fmt.Errorf("mulai transaksi dry run: %w", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), rollbackGraceTime)
		defer cleanupCancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	for _, setting := range []string{
		"SET LOCAL standard_conforming_strings = on",
		"SET LOCAL statement_timeout = '5s'",
		"SET LOCAL lock_timeout = '2s'",
	} {
		if _, err := tx.Exec(runCtx, setting); err != nil {
			return fmt.Errorf("set timeout dry run: %w", err)
		}
	}
	if _, err := tx.Exec(runCtx, spec.SQLUp, pgx.QueryExecModeSimpleProtocol); err != nil {
		classifyDryRunError("SQL up", runCtx, err, res)
		return nil
	}
	if tx.Conn().PgConn().TxStatus() != 'T' {
		res.Errors = append(res.Errors, "Dry run SQL up menutup transaksi secara tidak sah")
		return nil
	}
	if !spec.Irreversible && spec.SQLDown != nil {
		if _, err := tx.Exec(runCtx, *spec.SQLDown, pgx.QueryExecModeSimpleProtocol); err != nil {
			classifyDryRunError("SQL down", runCtx, err, res)
			return nil
		}
		if tx.Conn().PgConn().TxStatus() != 'T' {
			res.Errors = append(res.Errors, "Dry run SQL down menutup transaksi secara tidak sah")
			return nil
		}
	}
	return nil
}

func classifyDryRunError(stage string, ctx context.Context, err error, res *ValidationResult) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("Dry run %s melewati batas waktu", stage))
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "57014" || pgErr.Code == "55P03" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("Dry run %s melewati batas waktu atau menunggu lock (%s)", stage, pgErr.Code))
			return
		}
		res.Errors = append(res.Errors, fmt.Sprintf("Dry run %s gagal (%s): %s", stage, pgErr.Code, pgErr.Message))
		return
	}
	res.Errors = append(res.Errors, fmt.Sprintf("Dry run %s gagal: %v", stage, err))
}
