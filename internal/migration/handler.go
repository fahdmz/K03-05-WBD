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
	mux.HandleFunc("POST /api/databases/{id}/explain", h.explain) // buat bonus ya ma
	mux.HandleFunc("GET /api/databases/{id}/migrations", h.list)
	mux.HandleFunc("POST /api/databases/{id}/migrations", h.create)
	mux.HandleFunc("GET /api/migrations/{id}", h.get)
	mux.HandleFunc("PATCH /api/migrations/{id}", h.update)
	mux.HandleFunc("POST /api/migrations/{id}/validate", h.validate)
	mux.HandleFunc("POST /api/migrations/{id}/submit", h.submit)
}

type Revision struct {
	ID                 int64      `json:"id"`
	MigrationID        int64      `json:"migrationId"`
	RevisionNo         int        `json:"revisionNo"`
	Status             string     `json:"status"`
	SQLUp              string     `json:"sqlUp"`
	SQLDown            *string    `json:"sqlDown"`
	Irreversible       bool       `json:"irreversible"`
	IrreversibleReason *string    `json:"irreversibleReason"`
	Mode               string     `json:"mode"`
	BatchTable         *string    `json:"batchTable"`
	BatchColumn        *string    `json:"batchColumn"`
	BatchSize          *int       `json:"batchSize"`
	CreatedBy          string     `json:"createdBy"`
	CreatedAt          time.Time  `json:"createdAt"`
	SubmittedAt        *time.Time `json:"submittedAt"`
}

func (r Revision) toSpec() RevisionSpec {
	return RevisionSpec{
		Mode:               r.Mode,
		SQLUp:              r.SQLUp,
		SQLDown:            r.SQLDown,
		Irreversible:       r.Irreversible,
		IrreversibleReason: r.IrreversibleReason,
		BatchTable:         r.BatchTable,
		BatchColumn:        r.BatchColumn,
		BatchSize:          r.BatchSize,
	}
}

type ChangeRequestSummary struct {
	ID                int64           `json:"id"`
	RevisionID        int64           `json:"revisionId"`
	Action            string          `json:"action"`
	TargetExecutionID *string         `json:"targetExecutionId"`
	Status            string          `json:"status"`
	Validation        json.RawMessage `json:"validation"`
	RequestedBy       string          `json:"requestedBy"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

type MigrationSummary struct {
	ID                  int64     `json:"id"`
	DatabaseID          int64     `json:"databaseId"`
	Version             *int      `json:"version"`
	IsApplied           bool      `json:"isApplied"`
	Title               string    `json:"title"`
	CreatedBy           string    `json:"createdBy"`
	CreatedAt           time.Time `json:"createdAt"`
	CurrentRevision     *Revision `json:"currentRevision"`
	LatestRequestStatus *string   `json:"latestRequestStatus"`
}

type MigrationDetail struct {
	MigrationSummary
	Revisions      []Revision             `json:"revisions"`
	ChangeRequests []ChangeRequestSummary `json:"changeRequests"`
}

type migrationInput struct {
	Title              string  `json:"title"`
	Mode               string  `json:"mode"`
	SQLUp              string  `json:"sqlUp"`
	SQLDown            *string `json:"sqlDown"`
	Irreversible       bool    `json:"irreversible"`
	IrreversibleReason *string `json:"irreversibleReason"`
	BatchTable         *string `json:"batchTable"`
	BatchColumn        *string `json:"batchColumn"`
	BatchSize          *int    `json:"batchSize"`
}

type rowScanner interface {
	Scan(dest ...any) error
}

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

	result := make([]MigrationSummary, 0)
	for rows.Next() {
		item, err := scanMigrationSummary(rows)
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
	tx, err := h.platform.Begin(r.Context())
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membuat draft migration")
		return
	}
	defer tx.Rollback(r.Context())
	var migrationID int64
	err = tx.QueryRow(r.Context(), `
		INSERT INTO migrations (db_connection_id, title, sql, status, created_by)
		VALUES ($1, $2, $3, 'draft', $4)
		RETURNING id
	`, dbID, input.Title, input.SQLUp, user.ID).Scan(&migrationID)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membuat draft migration")
		return
	}
	var revisionID int64
	err = tx.QueryRow(r.Context(), `
		INSERT INTO migration_revisions (
			migration_id, revision_no, status, sql_up, sql_down,
			irreversible, irreversible_reason, mode,
			batch_table, batch_column, batch_size, created_by
		) VALUES ($1, 1, 'draft', $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id
	`,
		migrationID, input.SQLUp, input.SQLDown,
		input.Irreversible, input.IrreversibleReason, input.Mode,
		input.BatchTable, input.BatchColumn, input.BatchSize, user.ID,
	).Scan(&revisionID)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membuat revisi migration")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE migrations SET current_revision_id = $2 WHERE id = $1
	`, migrationID, revisionID); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal menautkan revisi migration")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal menyimpan draft migration")
		return
	}
	detail, err := h.loadMigrationDetail(r.Context(), h.platform, migrationID)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Draft migration berhasil dibuat tetapi gagal dibaca")
		return
	}
	web.WriteJSON(w, http.StatusCreated, detail)
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
	item, err := h.loadMigrationDetail(r.Context(), h.platform, id)
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

	dbID, err := h.migrationDatabaseID(r.Context(), id)
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
	input, ok := decodeMigrationInput(w, r)
	if !ok {
		return
	}
	tx, err := h.platform.Begin(r.Context())
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal memulai transaksi")
		return
	}
	defer tx.Rollback(r.Context())
	var version pgtype.Int4
	var currentRevID pgtype.Int8
	err = tx.QueryRow(r.Context(), `
		SELECT version, current_revision_id
		FROM migrations
		WHERE id = $1
		FOR UPDATE
	`, id).Scan(&version, &currentRevID)
	if errors.Is(err, pgx.ErrNoRows) {
		web.WriteError(w, http.StatusNotFound, "Migration tidak ditemukan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal mengunci migration")
		return
	}
	var revNo int
	var revStatus string
	if currentRevID.Valid {
		err = tx.QueryRow(r.Context(), `
			SELECT revision_no, status
			FROM migration_revisions
			WHERE id = $1
			FOR UPDATE
		`, currentRevID.Int64).Scan(&revNo, &revStatus)
		if err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal membaca revisi migration")
			return
		}
	}
	if currentRevID.Valid && revStatus == "draft" {
		if _, err := tx.Exec(r.Context(), `
			UPDATE migration_revisions
			SET sql_up = $2, sql_down = $3, irreversible = $4, irreversible_reason = $5,
			    mode = $6, batch_table = $7, batch_column = $8, batch_size = $9
			WHERE id = $1 AND status = 'draft'
		`,
			currentRevID.Int64, input.SQLUp, input.SQLDown,
			input.Irreversible, input.IrreversibleReason, input.Mode,
			input.BatchTable, input.BatchColumn, input.BatchSize,
		); err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal mengubah revisi draft")
			return
		}
		if _, err := tx.Exec(r.Context(), `
			UPDATE migrations SET title = $2, sql = $3 WHERE id = $1
		`, id, input.Title, input.SQLUp); err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal mengubah migration")
			return
		}
	} else {
		var hasHistory bool
		if err := tx.QueryRow(r.Context(), `
			SELECT EXISTS (SELECT 1 FROM migration_history WHERE migration_id = $1)
		`, id).Scan(&hasHistory); err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal memeriksa riwayat migration")
			return
		}
		if version.Valid || hasHistory {
			web.WriteError(w, http.StatusBadRequest, "Migration yang pernah dieksekusi bersifat immutable; buat migration baru")
			return
		}
		var newRevID int64
		err = tx.QueryRow(r.Context(), `
			INSERT INTO migration_revisions (
				migration_id, revision_no, status, sql_up, sql_down,
				irreversible, irreversible_reason, mode,
				batch_table, batch_column, batch_size, created_by
			) VALUES ($1, $2, 'draft', $3, $4, $5, $6, $7, $8, $9, $10, $11)
			RETURNING id
		`,
			id, revNo+1, input.SQLUp, input.SQLDown,
			input.Irreversible, input.IrreversibleReason, input.Mode,
			input.BatchTable, input.BatchColumn, input.BatchSize, user.ID,
		).Scan(&newRevID)
		if err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal membuat revisi baru")
			return
		}
		if _, err := tx.Exec(r.Context(), `
			UPDATE migrations SET title = $2, sql = $3, current_revision_id = $4 WHERE id = $1
		`, id, input.Title, input.SQLUp, newRevID); err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal menautkan revisi baru")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal menyimpan perubahan migration")
		return
	}
	detail, err := h.loadMigrationDetail(r.Context(), h.platform, id)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Migration berhasil diubah tetapi gagal dibaca")
		return
	}
	web.WriteJSON(w, http.StatusOK, detail)
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
	if input.Title == "" || strings.TrimSpace(input.SQLUp) == "" {
		web.WriteError(w, http.StatusBadRequest, "Title dan SQL wajib diisi")
		return migrationInput{}, false
	}
	if utf8.RuneCountInString(input.Title) > 200 {
		web.WriteError(w, http.StatusBadRequest, "Title maksimal 200 karakter")
		return migrationInput{}, false
	}
	input.Mode = strings.TrimSpace(input.Mode)
	if input.Mode == "" {
		input.Mode = "normal"
	}
	if input.Mode != "normal" && input.Mode != "batch" {
		web.WriteError(w, http.StatusBadRequest, "Mode migration harus 'normal' atau 'batch'")
		return migrationInput{}, false
	}
	empty := ""
	if input.Irreversible {
		if input.IrreversibleReason == nil {
			input.IrreversibleReason = &empty
		}
	} else {
		if input.SQLDown == nil {
			input.SQLDown = &empty
		}
	}
	if input.Mode == "normal" {
		input.BatchTable = nil
		input.BatchColumn = nil
		input.BatchSize = nil
	} else {
		if input.BatchTable == nil || strings.TrimSpace(*input.BatchTable) == "" ||
			input.BatchColumn == nil || strings.TrimSpace(*input.BatchColumn) == "" ||
			input.BatchSize == nil || *input.BatchSize < 1 || *input.BatchSize > maxBatchSize {
			web.WriteError(w, http.StatusBadRequest, "Mode batch wajib menyertakan batchTable, batchColumn, dan batchSize (1-100000)")
			return migrationInput{}, false
		}
		t := strings.TrimSpace(*input.BatchTable)
		c := strings.TrimSpace(*input.BatchColumn)
		input.BatchTable = &t
		input.BatchColumn = &c
	}
	return input, true
}
