package migration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/fahdmz/K03-05-WBD/internal/authz"
	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"
	"github.com/fahdmz/K03-05-WBD/internal/web"
	"github.com/fahdmz/K03-05-WBD/internal/workflow"
	"github.com/jackc/pgx/v5"
)

func (h *Handler) validate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	user, ok := h.requireUser(w, r)
	if !ok {
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
	rev, err := h.loadCurrentRevision(r.Context(), h.platform, id)
	if errors.Is(err, errMigrationNotFound) {
		web.WriteError(w, http.StatusNotFound, "Revisi migration tidak ditemukan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca revisi migration")
		return
	}
	if rev.Status != "draft" {
		web.WriteError(w, http.StatusBadRequest, "Hanya revisi draft yang dapat divalidasi")
		return
	}
	result, ok := h.runRevisionValidation(w, r.Context(), dbID, rev)
	if !ok {
		return
	}
	web.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	user, ok := h.requireUser(w, r)
	if !ok {
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
	rev, err := h.loadCurrentRevision(r.Context(), h.platform, id)
	if errors.Is(err, errMigrationNotFound) {
		web.WriteError(w, http.StatusNotFound, "Revisi migration tidak ditemukan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca revisi migration")
		return
	}
	if rev.Status != "draft" {
		web.WriteError(w, http.StatusBadRequest, "Hanya revisi draft yang dapat diajukan")
		return
	}
	valResult, ok := h.runRevisionValidation(w, r.Context(), dbID, rev)
	if !ok {
		return
	}
	if !valResult.Valid {
		web.WriteError(w, http.StatusBadRequest, strings.Join(valResult.Errors, "; "))
		return
	}
	valJSON, err := json.Marshal(valResult)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal menyerialisasi hasil validasi")
		return
	}
	tx, err := h.platform.Begin(r.Context())
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal memulai transaksi submit")
		return
	}
	defer tx.Rollback(r.Context())
	var currentRevID int64
	var currentStatus string
	err = tx.QueryRow(r.Context(), `
		SELECT r.id, r.status
		FROM migrations m
		JOIN migration_revisions r ON r.id = m.current_revision_id
		WHERE m.id = $1
		FOR UPDATE OF m, r
	`, id).Scan(&currentRevID, &currentStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		web.WriteError(w, http.StatusNotFound, "Migration tidak ditemukan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal mengunci revisi migration")
		return
	}
	if currentRevID != rev.ID || currentStatus != "draft" {
		web.WriteError(w, http.StatusConflict, "Revisi migration sudah berubah atau sudah diajukan")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE migration_revisions
		SET status = 'submitted', submitted_at = now()
		WHERE id = $1 AND status = 'draft'
	`, rev.ID); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membekukan revisi migration")
		return
	}
	crID, err := workflow.CreateRequest(r.Context(), tx, workflow.RequestInput{
		DatabaseID:  dbID,
		MigrationID: id,
		RevisionID:  rev.ID,
		Action:      "apply",
		RequestedBy: user.ID,
	})
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membuat change request")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE change_requests SET validation = $2 WHERE id = $1
	`, crID, valJSON); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal menyimpan hasil validasi")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal menyimpan pengajuan migration")
		return
	}
	detail, err := h.loadMigrationDetail(r.Context(), h.platform, id)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Pengajuan berhasil tetapi gagal membaca detail migration")
		return
	}
	web.WriteJSON(w, http.StatusCreated, map[string]any{
		"changeRequestId": crID,
		"migration":       detail,
		"validation":      valResult,
	})
}

func (h *Handler) runRevisionValidation(
	w http.ResponseWriter,
	ctx context.Context,
	dbID int64,
	rev Revision,
) (ValidationResult, bool) {
	spec := rev.toSpec()
	structRes := ValidateStructure(spec)
	if !structRes.Valid {
		return structRes, true
	}
	if h.manager == nil {
		web.WriteError(w, http.StatusInternalServerError, "Database manager belum dikonfigurasi")
		return ValidationResult{}, false
	}
	targetPool, err := h.manager.Pool(ctx, dbID)
	if err != nil {
		if errors.Is(err, dbmanager.ErrDatabaseNotFound) {
			web.WriteError(w, http.StatusNotFound, "Database tidak ditemukan")
			return ValidationResult{}, false
		}
		web.WriteError(w, http.StatusBadGateway, "Koneksi ke database target gagal")
		return ValidationResult{}, false
	}
	res, err := ValidateRevision(ctx, targetPool, spec)
	if err != nil {
		web.WriteError(w, http.StatusBadGateway, "Gagal memvalidasi migration pada database target")
		return ValidationResult{}, false
	}
	return res, true
}
