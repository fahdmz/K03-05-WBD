package migration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/authz"
	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"
	"github.com/fahdmz/K03-05-WBD/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxRequestBodyBytes = 1 << 20
const maxStoredErrorBytes = 2000

var errMigrationNotFound = errors.New("migration not found")

type Handler struct {
	platform   *pgxpool.Pool
	manager    dbmanager.Manager
	authorizer authz.Authorizer
}

func NewHandler(platform *pgxpool.Pool, manager dbmanager.Manager, authorizer authz.Authorizer) *Handler {
	return &Handler{platform: platform, manager: manager, authorizer: authorizer}
}

// Register pasang semua route API buat migration
// Middleware login dipasang dari luar fungsi ini.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/databases/{id}/migrations", h.list)
	mux.HandleFunc("POST /api/databases/{id}/migrations", h.create)
	mux.HandleFunc("GET /api/migrations/{id}", h.get)
	mux.HandleFunc("PATCH /api/migrations/{id}", h.update)
	mux.HandleFunc("POST /api/migrations/{id}/execute", h.execute)
}

type Migration struct {
	ID           int64      `json:"id"`
	DatabaseID   int64      `json:"databaseId"`
	Version      *int       `json:"version"`
	Title        string     `json:"title"`
	SQL          string     `json:"sql"`
	Status       string     `json:"status"`
	CreatedBy    string     `json:"createdBy"`
	ExecutedBy   *string    `json:"executedBy"`
	CreatedAt    time.Time  `json:"createdAt"`
	ExecutedAt   *time.Time `json:"executedAt"`
	ErrorMessage *string    `json:"errorMessage"`
}

type migrationInput struct {
	Title string `json:"title"`
	SQL   string `json:"sql"`
}

type rowScanner interface {
	Scan(dest ...any) error
}

const migrationSelect = `
	SELECT m.id, m.db_connection_id, m.version, m.title, m.sql, m.status,
	       creator.username, executor.username, m.created_at, m.executed_at, m.error_message
	FROM migrations m
	JOIN users creator ON creator.id = m.created_by
	LEFT JOIN users executor ON executor.id = m.executed_by
`

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	dbID, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	_, ok = h.requireAccess(w, r, dbID, authz.View)
	if !ok {
		return
	}
	if !h.databaseExists(w, r.Context(), dbID) {
		return
	}

	rows, err := h.platform.Query(r.Context(), migrationSelect+`
		WHERE m.db_connection_id = $1
		ORDER BY m.version ASC NULLS LAST, m.created_at ASC, m.id ASC`, dbID)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca riwayat migration")
		return
	}
	defer rows.Close()

	result := make([]Migration, 0)
	for rows.Next() {
		item, err := scanMigration(rows)
		if err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal membaca riwayat migration")
			return
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca riwayat migration")
		return
	}
	web.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	dbID, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	user, ok := h.requireAccess(w, r, dbID, authz.Edit)
	if !ok {
		return
	}
	input, ok := decodeMigrationInput(w, r)
	if !ok {
		return
	}
	if !h.databaseExists(w, r.Context(), dbID) {
		return
	}

	var id int64
	err := h.platform.QueryRow(r.Context(), `
		INSERT INTO migrations (db_connection_id, title, sql, status, created_by)
		VALUES ($1, $2, $3, 'draft', $4)
		RETURNING id`, dbID, input.Title, input.SQL, user.ID).Scan(&id)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membuat draft migration")
		return
	}
	item, err := h.loadMigration(r.Context(), h.platform, id)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Draft migration berhasil dibuat tetapi gagal dibaca")
		return
	}
	web.WriteJSON(w, http.StatusCreated, item)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if !h.configured(w) {
		return
	}

	dbID, err := h.migrationDatabaseID(r.Context(), id)
	if errors.Is(err, errMigrationNotFound) {
		web.WriteError(w, http.StatusNotFound, "Migration tidak ditemukan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca migration")
		return
	}
	if !h.authorize(w, r.Context(), user.ID, dbID, authz.View) {
		return
	}
	item, err := h.loadMigration(r.Context(), h.platform, id)
	if errors.Is(err, errMigrationNotFound) {
		web.WriteError(w, http.StatusNotFound, "Migration tidak ditemukan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca migration")
		return
	}
	web.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if !h.configured(w) {
		return
	}

	dbID, status, err := h.migrationLocation(r.Context(), id)
	if errors.Is(err, errMigrationNotFound) {
		web.WriteError(w, http.StatusNotFound, "Migration tidak ditemukan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca migration")
		return
	}
	if !h.authorize(w, r.Context(), user.ID, dbID, authz.Edit) {
		return
	}
	if status != "draft" {
		web.WriteError(w, http.StatusBadRequest, "Hanya draft yang bisa diubah")
		return
	}
	input, ok := decodeMigrationInput(w, r)
	if !ok {
		return
	}

	tag, err := h.platform.Exec(r.Context(), `
		UPDATE migrations SET title = $2, sql = $3
		WHERE id = $1 AND status = 'draft'`, id, input.Title, input.SQL)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal mengubah draft migration")
		return
	}
	if tag.RowsAffected() == 0 {
		web.WriteError(w, http.StatusBadRequest, "Hanya draft yang bisa diubah")
		return
	}
	item, err := h.loadMigration(r.Context(), h.platform, id)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Draft migration berhasil diubah tetapi gagal dibaca")
		return
	}
	web.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) execute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if !h.configured(w) {
		return
	}
	if h.manager == nil {
		web.WriteError(w, http.StatusInternalServerError, "Migration belum dikonfigurasi")
		return
	}

	dbID, _, err := h.migrationLocation(r.Context(), id)
	if errors.Is(err, errMigrationNotFound) {
		web.WriteError(w, http.StatusNotFound, "Migration tidak ditemukan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca migration")
		return
	}
	if !h.authorize(w, r.Context(), user.ID, dbID, authz.Edit) {
		return
	}

	platformTx, err := h.platform.Begin(r.Context())
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal memulai pencatatan migration")
		return
	}
	defer platformTx.Rollback(r.Context())

	var lockedDBID int64
	var status, sql string
	err = platformTx.QueryRow(r.Context(), `
		SELECT db_connection_id, status, sql
		FROM migrations WHERE id = $1 FOR UPDATE`, id,
	).Scan(&lockedDBID, &status, &sql)
	if errors.Is(err, pgx.ErrNoRows) {
		web.WriteError(w, http.StatusNotFound, "Migration tidak ditemukan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal mengunci migration")
		return
	}
	if lockedDBID != dbID {
		web.WriteError(w, http.StatusInternalServerError, "Database migration berubah")
		return
	}
	if status != "draft" {
		web.WriteError(w, http.StatusBadRequest, "Hanya draft yang bisa dieksekusi")
		return
	}

	// Kunci database target biar migration yang jalan barengan nggak tabrakan
	// Nomor versi juga ditentuin saat kuncinya masih aktif
	var lockedConnectionID int64
	err = platformTx.QueryRow(r.Context(), `
		SELECT id FROM db_connections WHERE id = $1 FOR UPDATE`, dbID,
	).Scan(&lockedConnectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		web.WriteError(w, http.StatusNotFound, "Database tidak ditemukan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal mengunci database")
		return
	}

	targetPool, err := h.manager.Pool(r.Context(), dbID)
	if err != nil {
		if errors.Is(err, dbmanager.ErrDatabaseNotFound) {
			web.WriteError(w, http.StatusNotFound, "Database tidak ditemukan")
			return
		}
		if !h.recordFailure(w, r.Context(), platformTx, id, user.ID, err) {
			return
		}
		web.WriteError(w, http.StatusBadGateway, "Koneksi ke database target gagal")
		return
	}
	targetTx, err := targetPool.Begin(r.Context())
	if err != nil {
		if !h.recordFailure(w, r.Context(), platformTx, id, user.ID, err) {
			return
		}
		web.WriteError(w, http.StatusBadGateway, "Koneksi ke database target gagal")
		return
	}
	defer targetTx.Rollback(r.Context())

	if _, err := targetTx.Exec(r.Context(), sql, pgx.QueryExecModeSimpleProtocol); err != nil {
		if !h.recordFailure(w, r.Context(), platformTx, id, user.ID, err) {
			return
		}
		item, loadErr := h.loadMigration(r.Context(), h.platform, id)
		if loadErr != nil {
			web.WriteError(w, http.StatusInternalServerError, "Migration gagal dan statusnya tidak dapat dibaca")
			return
		}
		web.WriteJSON(w, http.StatusOK, item)
		return
	}
	if err := targetTx.Commit(r.Context()); err != nil {
		if !h.recordFailure(w, r.Context(), platformTx, id, user.ID, err) {
			return
		}
		web.WriteError(w, http.StatusBadGateway, "Commit migration pada database target gagal")
		return
	}

	var version int
	err = platformTx.QueryRow(r.Context(), `
		SELECT COALESCE(MAX(version), 0) + 1
		FROM migrations
		WHERE db_connection_id = $1 AND status = 'success'`, dbID,
	).Scan(&version)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Migration berhasil pada database target tetapi gagal memberi versi")
		return
	}
	tag, err := platformTx.Exec(r.Context(), `
		UPDATE migrations
		SET version = $2, status = 'success', executed_by = $3,
		    executed_at = now(), error_message = NULL
		WHERE id = $1 AND status = 'draft'`, id, version, user.ID)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Migration berhasil pada database target tetapi gagal mencatat riwayat")
		return
	}
	if tag.RowsAffected() != 1 {
		web.WriteError(w, http.StatusInternalServerError, "Migration berhasil pada database target tetapi status riwayat tidak berubah")
		return
	}
	if err := platformTx.Commit(r.Context()); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Migration berhasil pada database target tetapi gagal mencatat riwayat")
		return
	}

	item, err := h.loadMigration(r.Context(), h.platform, id)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Migration berhasil tetapi gagal membaca riwayat")
		return
	}
	web.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) recordFailure(w http.ResponseWriter, ctx context.Context, tx pgx.Tx, id, userID int64, cause error) bool {
	message := cause.Error()
	if len(message) > maxStoredErrorBytes {
		message = message[:maxStoredErrorBytes]
	}
	_, err := tx.Exec(ctx, `
		UPDATE migrations
		SET status = 'failed', version = NULL, executed_by = $2,
		    executed_at = now(), error_message = $3
		WHERE id = $1 AND status = 'draft'`, id, userID, message)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Migration gagal dan statusnya tidak dapat dicatat")
		return false
	}
	if err := tx.Commit(ctx); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Migration gagal dan statusnya tidak dapat dicatat")
		return false
	}
	return true
}

func (h *Handler) loadMigration(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id int64) (Migration, error) {
	item, err := scanMigration(queryer.QueryRow(ctx, migrationSelect+` WHERE m.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Migration{}, errMigrationNotFound
	}
	return item, err
}

func scanMigration(row rowScanner) (Migration, error) {
	var item Migration
	var version pgtype.Int4
	var executedBy pgtype.Text
	var executedAt pgtype.Timestamptz
	var errorMessage pgtype.Text
	if err := row.Scan(
		&item.ID, &item.DatabaseID, &version, &item.Title, &item.SQL, &item.Status,
		&item.CreatedBy, &executedBy, &item.CreatedAt, &executedAt, &errorMessage,
	); err != nil {
		return Migration{}, err
	}
	if version.Valid {
		v := int(version.Int32)
		item.Version = &v
	}
	if executedBy.Valid {
		item.ExecutedBy = &executedBy.String
	}
	if executedAt.Valid {
		t := executedAt.Time
		item.ExecutedAt = &t
	}
	if errorMessage.Valid {
		item.ErrorMessage = &errorMessage.String
	}
	return item, nil
}

func (h *Handler) migrationDatabaseID(ctx context.Context, id int64) (int64, error) {
	var dbID int64
	err := h.platform.QueryRow(ctx, `SELECT db_connection_id FROM migrations WHERE id = $1`, id).Scan(&dbID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errMigrationNotFound
	}
	return dbID, err
}

func (h *Handler) migrationLocation(ctx context.Context, id int64) (int64, string, error) {
	var dbID int64
	var status string
	err := h.platform.QueryRow(ctx, `SELECT db_connection_id, status FROM migrations WHERE id = $1`, id).Scan(&dbID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", errMigrationNotFound
	}
	return dbID, status, err
}

func (h *Handler) databaseExists(w http.ResponseWriter, ctx context.Context, id int64) bool {
	var exists bool
	if err := h.platform.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM db_connections WHERE id = $1)`, id).Scan(&exists); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal memeriksa database")
		return false
	}
	if !exists {
		web.WriteError(w, http.StatusNotFound, "Database tidak ditemukan")
		return false
	}
	return true
}

func (h *Handler) requireAccess(w http.ResponseWriter, r *http.Request, dbID int64, level authz.Level) (*auth.User, bool) {
	user, ok := h.requireUser(w, r)
	if !ok || !h.authorize(w, r.Context(), user.ID, dbID, level) {
		return nil, false
	}
	return user, true
}

func (h *Handler) requireUser(w http.ResponseWriter, r *http.Request) (*auth.User, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		web.WriteError(w, http.StatusUnauthorized, "Belum login")
		return nil, false
	}
	if !h.configured(w) {
		return nil, false
	}
	return user, true
}

func (h *Handler) configured(w http.ResponseWriter) bool {
	if h.platform == nil || h.authorizer == nil {
		web.WriteError(w, http.StatusInternalServerError, "Migration belum dikonfigurasi")
		return false
	}
	return true
}

func (h *Handler) authorize(w http.ResponseWriter, ctx context.Context, userID, dbID int64, level authz.Level) bool {
	if err := h.authorizer.Require(ctx, userID, dbID, level); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			web.WriteError(w, http.StatusForbidden, "Tidak punya akses ke database ini")
		} else {
			web.WriteError(w, http.StatusInternalServerError, "Gagal memeriksa akses database")
		}
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request, key string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	if err != nil || id < 1 {
		web.WriteError(w, http.StatusBadRequest, "ID tidak valid")
		return 0, false
	}
	return id, true
}

func decodeMigrationInput(w http.ResponseWriter, r *http.Request) (migrationInput, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input migrationInput
	if err := decoder.Decode(&input); err != nil {
		web.WriteError(w, http.StatusBadRequest, "Request migration tidak valid")
		return migrationInput{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		web.WriteError(w, http.StatusBadRequest, "Request migration tidak valid")
		return migrationInput{}, false
	}
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" || strings.TrimSpace(input.SQL) == "" {
		web.WriteError(w, http.StatusBadRequest, "Title dan SQL wajib diisi")
		return migrationInput{}, false
	}
	if utf8.RuneCountInString(input.Title) > 200 {
		web.WriteError(w, http.StatusBadRequest, "Title maksimal 200 karakter")
		return migrationInput{}, false
	}
	return input, true
}
