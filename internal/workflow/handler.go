package workflow

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
	"github.com/fahdmz/K03-05-WBD/internal/obs"
	"github.com/fahdmz/K03-05-WBD/internal/queue"
	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/fahdmz/K03-05-WBD/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxRequestBodyBytes = 1 << 20

type EnqueueFunc func(ctx context.Context, tx pgx.Tx, changeRequestID int64, trace map[string]string) (int64, string, error)

type Handler struct {
	platform   *pgxpool.Pool
	authorizer authz.Authorizer
	enqueue    EnqueueFunc
}

func NewHandler(platform *pgxpool.Pool, authorizer authz.Authorizer) *Handler {
	return &Handler{
		platform:   platform,
		authorizer: authorizer,
		enqueue:    queue.Enqueue,
	}
}

func (h *Handler) WithEnqueue(fn EnqueueFunc) *Handler {
	cp := *h
	cp.enqueue = fn
	return &cp
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/databases/{id}/change-requests", h.listByDatabase)
	mux.HandleFunc("GET /api/reviews", h.listReviews)
	mux.HandleFunc("GET /api/change-requests/{id}", h.get)
	mux.HandleFunc("POST /api/change-requests/{id}/approve", h.approve)
	mux.HandleFunc("POST /api/change-requests/{id}/reject", h.reject)
	mux.HandleFunc("POST /api/change-requests/{id}/run", h.run)
}

type ChangeRequestItem struct {
	ID                int64           `json:"id"`
	DatabaseID        int64           `json:"databaseId"`
	DatabaseName      string          `json:"databaseName"`
	MigrationID       int64           `json:"migrationId"`
	MigrationTitle    string          `json:"migrationTitle"`
	RevisionID        int64           `json:"revisionId"`
	RevisionNo        int             `json:"revisionNo"`
	Action            string          `json:"action"`
	TargetExecutionID *string         `json:"targetExecutionId"`
	Status            string          `json:"status"`
	Validation        json.RawMessage `json:"validation"`
	RequestedBy       string          `json:"requestedBy"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

type ReviewItem struct {
	ChangeRequestItem
	PendingGroups []string `json:"pendingGroups"`
}

type ChangeRequestDetail struct {
	ChangeRequestItem
	Mode               string  `json:"mode"`
	SQLUp              string  `json:"sqlUp"`
	SQLDown            *string `json:"sqlDown"`
	Irreversible       bool    `json:"irreversible"`
	IrreversibleReason *string `json:"irreversibleReason"`
	BatchTable         *string `json:"batchTable"`
	BatchColumn        *string `json:"batchColumn"`
	BatchSize          *int    `json:"batchSize"`
	Approval           Status  `json:"approval"`
}

func (h *Handler) listByDatabase(w http.ResponseWriter, r *http.Request) {
	dbID, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r.Context(), user.ID, dbID, authz.View) {
		return
	}

	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	if statusFilter != "" && !validRequestStatus(statusFilter) {
		web.WriteError(w, http.StatusBadRequest, "Filter status tidak valid")
		return
	}

	rows, err := h.platform.Query(r.Context(), `
		SELECT
			cr.id, cr.db_connection_id, d.name,
			cr.migration_id, m.title,
			cr.revision_id, rev.revision_no,
			cr.action, cr.target_execution_id::text, cr.status,
			cr.validation, u.username, cr.created_at, cr.updated_at
		FROM change_requests cr
		JOIN db_connections d ON d.id = cr.db_connection_id
		JOIN migrations m ON m.id = cr.migration_id
		JOIN migration_revisions rev ON rev.id = cr.revision_id
		JOIN users u ON u.id = cr.requested_by
		WHERE cr.db_connection_id = $1
		  AND ($2 = '' OR cr.status = $2)
		ORDER BY cr.created_at DESC, cr.id DESC
	`, dbID, statusFilter)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca daftar change request")
		return
	}
	defer rows.Close()

	items := make([]ChangeRequestItem, 0)
	for rows.Next() {
		item, err := scanChangeRequestItem(rows)
		if err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal membaca daftar change request")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca daftar change request")
		return
	}
	web.WriteJSON(w, http.StatusOK, items)
}

func (h *Handler) listReviews(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	rows, err := h.platform.Query(r.Context(), `
		SELECT
			cr.id, cr.db_connection_id, d.name,
			cr.migration_id, m.title,
			cr.revision_id, rev.revision_no,
			cr.action, cr.target_execution_id::text, cr.status,
			cr.validation, u.username, cr.created_at, cr.updated_at,
			ARRAY_AGG(g.name ORDER BY g.id) AS pending_groups
		FROM change_requests cr
		JOIN db_connections d ON d.id = cr.db_connection_id
		JOIN migrations m ON m.id = cr.migration_id
		JOIN migration_revisions rev ON rev.id = cr.revision_id
		JOIN users u ON u.id = cr.requested_by
		JOIN group_db_access gda ON gda.db_connection_id = cr.db_connection_id
		JOIN groups g ON g.id = gda.group_id AND g.leader_user_id = $1
		LEFT JOIN approvals a
			ON a.change_request_id = cr.id
			AND a.group_id = g.id
			AND a.approver_id = g.leader_user_id
		WHERE cr.status = 'pending_review'
		  AND a.decision IS NULL
		GROUP BY cr.id, d.name, m.title, rev.revision_no, u.username
		ORDER BY cr.created_at ASC, cr.id ASC
	`, user.ID)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca daftar review")
		return
	}
	defer rows.Close()

	reviews := make([]ReviewItem, 0)
	for rows.Next() {
		var item ReviewItem
		var targetExec pgtype.Text
		var valBytes []byte
		if err := rows.Scan(
			&item.ID, &item.DatabaseID, &item.DatabaseName,
			&item.MigrationID, &item.MigrationTitle,
			&item.RevisionID, &item.RevisionNo,
			&item.Action, &targetExec, &item.Status,
			&valBytes, &item.RequestedBy, &item.CreatedAt, &item.UpdatedAt,
			&item.PendingGroups,
		); err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal membaca daftar review")
			return
		}
		if targetExec.Valid {
			item.TargetExecutionID = &targetExec.String
		}
		if len(valBytes) > 0 {
			item.Validation = json.RawMessage(valBytes)
		}
		reviews = append(reviews, item)
	}
	if err := rows.Err(); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca daftar review")
		return
	}
	web.WriteJSON(w, http.StatusOK, reviews)
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

	detail, err := h.loadDetail(r.Context(), h.platform, id)
	if errors.Is(err, ErrNotFound) {
		web.WriteError(w, http.StatusNotFound, "Change request tidak ditemukan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca change request")
		return
	}
	if !h.authorize(w, r.Context(), user.ID, detail.DatabaseID, authz.View) {
		return
	}
	web.WriteJSON(w, http.StatusOK, detail)
}

func (h *Handler) approve(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, "approve")
}

func (h *Handler) reject(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, "reject")
}

func (h *Handler) decide(w http.ResponseWriter, r *http.Request, decision string) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	var input struct {
		Comment string `json:"comment"`
	}
	if !decodeOptionalBody(w, r, &input) {
		return
	}
	input.Comment = strings.TrimSpace(input.Comment)
	if utf8.RuneCountInString(input.Comment) > 2000 {
		web.WriteError(w, http.StatusBadRequest, "Komentar maksimal 2000 karakter")
		return
	}
	if decision == "reject" && input.Comment == "" {
		web.WriteError(w, http.StatusBadRequest, "Alasan penolakan wajib diisi")
		return
	}

	tx, err := h.platform.Begin(r.Context())
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal memulai transaksi approval")
		return
	}
	defer tx.Rollback(r.Context())

	var dbID int64
	var status string
	err = tx.QueryRow(r.Context(), `
		SELECT db_connection_id, status
		FROM change_requests
		WHERE id = $1
		FOR UPDATE
	`, id).Scan(&dbID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		web.WriteError(w, http.StatusNotFound, "Change request tidak ditemukan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal mengunci change request")
		return
	}
	if status != "pending_review" && status != "approved" {
		web.WriteError(w, http.StatusBadRequest, "Change request tidak dapat direview pada status saat ini")
		return
	}

	var groupIDs []int64
	if err := tx.QueryRow(r.Context(), `
		SELECT ARRAY(
			SELECT g.id
			FROM group_db_access gda
			JOIN groups g ON g.id = gda.group_id
			WHERE gda.db_connection_id = $1
			  AND g.leader_user_id = $2
			ORDER BY g.id
		)
	`, dbID, user.ID).Scan(&groupIDs); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal memeriksa status leader")
		return
	}
	if len(groupIDs) == 0 {
		web.WriteError(w, http.StatusForbidden, "Hanya leader group yang memiliki akses ke database ini yang dapat memberi keputusan")
		return
	}

	for _, gid := range groupIDs {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO approvals (change_request_id, group_id, approver_id, decision, comment)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (change_request_id, group_id)
			DO UPDATE SET
				approver_id = EXCLUDED.approver_id,
				decision = EXCLUDED.decision,
				comment = EXCLUDED.comment,
				created_at = now()
		`, id, gid, user.ID, decision, input.Comment); err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal menyimpan keputusan approval")
			return
		}
	}

	nextStatus := "pending_review"
	if decision == "reject" {
		nextStatus = "rejected"
	} else {
		st, err := ApprovalStatus(r.Context(), tx, id)
		if err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal menghitung status approval")
			return
		}
		if st.Rejected {
			nextStatus = "rejected"
		} else if st.Approved {
			nextStatus = "approved"
		}
	}

	if _, err := tx.Exec(r.Context(), `
		UPDATE change_requests
		SET status = $2, updated_at = now()
		WHERE id = $1
	`, id, nextStatus); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal memperbarui status change request")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal menyimpan keputusan review")
		return
	}

	detail, err := h.loadDetail(r.Context(), h.platform, id)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Keputusan tersimpan tetapi gagal membaca detail")
		return
	}
	web.WriteJSON(w, http.StatusOK, detail)
}

func (h *Handler) run(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	tx, err := h.platform.Begin(r.Context())
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal memulai transaksi antrean")
		return
	}
	defer tx.Rollback(r.Context())

	var dbID int64
	var status string
	err = tx.QueryRow(r.Context(), `
		SELECT db_connection_id, status
		FROM change_requests
		WHERE id = $1
		FOR UPDATE
	`, id).Scan(&dbID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		web.WriteError(w, http.StatusNotFound, "Change request tidak ditemukan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal mengunci change request")
		return
	}

	if !h.authorize(w, r.Context(), user.ID, dbID, authz.Edit) {
		return
	}

	switch status {
	case "queued", "running", "success":
		web.WriteError(w, http.StatusConflict, "Change request sudah pernah diantrekan")
		return
	case "approved":
		// lanjut cek approval dinamis
	default:
		web.WriteError(w, http.StatusBadRequest, "Change request belum berstatus approved")
		return
	}

	st, err := ApprovalStatus(r.Context(), tx, id)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal memverifikasi status approval")
		return
	}
	if !st.Approved {
		// Akses group atau leader berubah setelah approved -> kembalikan ke pending_review.
		if _, err := tx.Exec(r.Context(), `
			UPDATE change_requests SET status = 'pending_review', updated_at = now() WHERE id = $1
		`, id); err == nil {
			_ = tx.Commit(r.Context())
		}
		web.WriteError(w, http.StatusConflict, "Approval belum lengkap karena perubahan akses atau leader; status kembali ke pending_review")
		return
	}

	enqueueFn := h.enqueue
	if enqueueFn == nil {
		enqueueFn = queue.Enqueue
	}
	jobID, execID, err := enqueueFn(r.Context(), tx, id, obs.InjectTrace(r.Context()))
	if errors.Is(err, queue.ErrNotApproved) {
		web.WriteError(w, http.StatusBadRequest, "Change request belum berstatus approved")
		return
	}
	if errors.Is(err, queue.ErrAlreadyQueued) {
		web.WriteError(w, http.StatusConflict, "Change request sudah pernah diantrekan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal memasukkan job ke antrean")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal menyimpan antrean job")
		return
	}

	web.WriteJSON(w, http.StatusAccepted, map[string]any{
		"jobId":       jobID,
		"executionId": execID,
	})
}

func (h *Handler) loadDetail(ctx context.Context, q store.Querier, id int64) (ChangeRequestDetail, error) {
	var d ChangeRequestDetail
	var targetExec, sqlDown, irrevReason, batchTable, batchCol pgtype.Text
	var batchSize pgtype.Int4
	var valBytes []byte

	err := q.QueryRow(ctx, `
		SELECT
			cr.id, cr.db_connection_id, db.name,
			cr.migration_id, m.title,
			cr.revision_id, rev.revision_no,
			cr.action, cr.target_execution_id::text, cr.status,
			cr.validation, u.username, cr.created_at, cr.updated_at,
			rev.mode, rev.sql_up, rev.sql_down, rev.irreversible, rev.irreversible_reason,
			rev.batch_table, rev.batch_column, rev.batch_size
		FROM change_requests cr
		JOIN db_connections db ON db.id = cr.db_connection_id
		JOIN migrations m ON m.id = cr.migration_id
		JOIN migration_revisions rev ON rev.id = cr.revision_id
		JOIN users u ON u.id = cr.requested_by
		WHERE cr.id = $1
	`, id).Scan(
		&d.ID, &d.DatabaseID, &d.DatabaseName,
		&d.MigrationID, &d.MigrationTitle,
		&d.RevisionID, &d.RevisionNo,
		&d.Action, &targetExec, &d.Status,
		&valBytes, &d.RequestedBy, &d.CreatedAt, &d.UpdatedAt,
		&d.Mode, &d.SQLUp, &sqlDown, &d.Irreversible, &irrevReason,
		&batchTable, &batchCol, &batchSize,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ChangeRequestDetail{}, ErrNotFound
	}
	if err != nil {
		return ChangeRequestDetail{}, err
	}
	if targetExec.Valid {
		d.TargetExecutionID = &targetExec.String
	}
	if len(valBytes) > 0 {
		d.Validation = json.RawMessage(valBytes)
	}
	if sqlDown.Valid {
		d.SQLDown = &sqlDown.String
	}
	if irrevReason.Valid {
		d.IrreversibleReason = &irrevReason.String
	}
	if batchTable.Valid {
		d.BatchTable = &batchTable.String
	}
	if batchCol.Valid {
		d.BatchColumn = &batchCol.String
	}
	if batchSize.Valid {
		bs := int(batchSize.Int32)
		d.BatchSize = &bs
	}

	st, err := ApprovalStatus(ctx, q, id)
	if err != nil {
		return ChangeRequestDetail{}, err
	}
	d.Approval = st
	return d, nil
}

func scanChangeRequestItem(row interface{ Scan(dest ...any) error }) (ChangeRequestItem, error) {
	var item ChangeRequestItem
	var targetExec pgtype.Text
	var valBytes []byte
	if err := row.Scan(
		&item.ID, &item.DatabaseID, &item.DatabaseName,
		&item.MigrationID, &item.MigrationTitle,
		&item.RevisionID, &item.RevisionNo,
		&item.Action, &targetExec, &item.Status,
		&valBytes, &item.RequestedBy, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return ChangeRequestItem{}, err
	}
	if targetExec.Valid {
		item.TargetExecutionID = &targetExec.String
	}
	if len(valBytes) > 0 {
		item.Validation = json.RawMessage(valBytes)
	}
	return item, nil
}

func (h *Handler) requireUser(w http.ResponseWriter, r *http.Request) (*auth.User, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		web.WriteError(w, http.StatusUnauthorized, "Belum login")
		return nil, false
	}
	if h.platform == nil || h.authorizer == nil {
		web.WriteError(w, http.StatusInternalServerError, "Workflow belum dikonfigurasi")
		return nil, false
	}
	return user, true
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

func decodeOptionalBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		return true
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		web.WriteError(w, http.StatusBadRequest, "Request JSON tidak valid")
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		web.WriteError(w, http.StatusBadRequest, "Request JSON tidak valid")
		return false
	}
	return true
}

func validRequestStatus(s string) bool {
	switch s {
	case "pending_review", "approved", "rejected", "queued",
		"running", "success", "failed", "partial", "uncertain":
		return true
	default:
		return false
	}
}
