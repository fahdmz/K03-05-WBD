package history

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/authz"
	"github.com/fahdmz/K03-05-WBD/internal/obs"
	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/fahdmz/K03-05-WBD/internal/web"
	"github.com/fahdmz/K03-05-WBD/internal/workflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errOpenRequest = errors.New("masih ada change request yang belum selesai untuk migration ini")

type Handler struct {
	platform   *pgxpool.Pool
	authorizer authz.Authorizer
}

func NewHandler(platform *pgxpool.Pool, authorizer authz.Authorizer) *Handler {
	return &Handler{platform: platform, authorizer: authorizer}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/migrations/{id}/rollback", h.rollback)
	mux.HandleFunc("POST /api/migrations/{id}/forward", h.forward)
	mux.HandleFunc("POST /api/jobs/{id}/resume", h.resume)
	mux.HandleFunc("POST /api/jobs/{id}/revert", h.revert)
	mux.HandleFunc("GET /api/jobs/{id}", h.getJob)
	mux.HandleFunc("GET /api/databases/{id}/jobs", h.listJobs)
	mux.HandleFunc("GET /api/databases/{id}/history", h.listHistory)
}

type Entry struct {
	ExecutionID        string    `json:"executionId"`
	Attempt            int       `json:"attempt"`
	MigrationID        int64     `json:"migrationId"`
	Title              string    `json:"title"`
	Version            *int      `json:"version"`
	RevisionNo         int       `json:"revisionNo"`
	Action             string    `json:"action"`
	Status             string    `json:"status"`
	RevertsExecutionID *string   `json:"revertsExecutionId"`
	ExecutedSQL        string    `json:"executedSql"`
	Mode               string    `json:"mode"`
	Batches            *int      `json:"batches"`
	RowsAffected       *int64    `json:"rowsAffected"`
	ErrorMessage       *string   `json:"errorMessage"`
	SQLState           *string   `json:"sqlstate"`
	RequestedBy        string    `json:"requestedBy"`
	StartedAt          time.Time `json:"startedAt"`
	FinishedAt         time.Time `json:"finishedAt"`
}

type Job struct {
	ID               int64      `json:"id"`
	ChangeRequestID  int64      `json:"changeRequestId"`
	DatabaseID       int64      `json:"databaseId"`
	MigrationID      int64      `json:"migrationId"`
	MigrationTitle   string     `json:"migrationTitle"`
	Action           string     `json:"action"`
	EffectiveAction  string     `json:"effectiveAction"`
	Direction        string     `json:"direction"`
	Mode             string     `json:"mode"`
	ExecutionID      string     `json:"executionId"`
	Status           string     `json:"status"`
	Attempt          int        `json:"attempt"`
	MaxAttempts      int        `json:"maxAttempts"`
	BatchesCommitted int        `json:"batchesCommitted"`
	RowsCommitted    int64      `json:"rowsCommitted"`
	LastError        *string    `json:"lastError"`
	LastSQLState     *string    `json:"lastSqlstate"`
	CreatedAt        time.Time  `json:"createdAt"`
	StartedAt        *time.Time `json:"startedAt"`
	FinishedAt       *time.Time `json:"finishedAt"`
	target           *string
}

func HideRollback(entries []Entry) []Entry {
	hidden := map[string]bool{}
	for _, e := range entries {
		if e.Action == string(Rollback) && e.Status == string(Succeeded) && e.RevertsExecutionID != nil {
			hidden[e.ExecutionID] = true
			hidden[*e.RevertsExecutionID] = true
		}
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Status != string(Succeeded) || !hidden[e.ExecutionID] {
			out = append(out, e)
		}
	}
	return out
}

func (h *Handler) rollback(w http.ResponseWriter, r *http.Request) {
	h.createPositionRequest(w, r, Rollback)
}

func (h *Handler) forward(w http.ResponseWriter, r *http.Request) {
	h.createPositionRequest(w, r, Forward)
}

func (h *Handler) createPositionRequest(w http.ResponseWriter, r *http.Request, action Action) {
	migrationID, ok := pathID(w, r)
	if !ok {
		return
	}
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	tx, err := h.platform.Begin(ctx)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal memulai transaksi")
		return
	}
	defer tx.Rollback(ctx)

	state, err := loadState(ctx, tx, migrationID)
	if err != nil {
		writeRuleError(w, err)
		return
	}
	if !h.authorize(w, ctx, user.ID, state.databaseID, authz.Edit) {
		return
	}

	var target Target
	if action == Rollback {
		target, err = CheckRollback(ctx, tx, migrationID)
	} else {
		target, err = CheckForward(ctx, tx, migrationID)
	}
	if err != nil {
		writeRuleError(w, err)
		return
	}
	if err := ensureNoOpenRequest(ctx, tx, migrationID, true); err != nil {
		writeRuleError(w, err)
		return
	}

	var targetExecution *string
	if target.ExecutionID != "" {
		targetExecution = &target.ExecutionID
	}
	requestID, err := workflow.CreateRequest(ctx, tx, workflow.RequestInput{
		DatabaseID:        target.DatabaseID,
		MigrationID:       migrationID,
		RevisionID:        target.RevisionID,
		Action:            string(action),
		TargetExecutionID: targetExecution,
		RequestedBy:       user.ID,
	})
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membuat change request")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal menyimpan change request")
		return
	}

	obs.Logger(ctx).Info("change_request.created",
		"actor", user.Username, "database", target.DatabaseID, "status", "pending_review",
		"change_request_id", requestID, "migration_id", migrationID, "action", string(action))
	web.WriteJSON(w, http.StatusCreated, map[string]any{"changeRequestId": requestID, "status": "pending_review"})
}

func (h *Handler) resume(w http.ResponseWriter, r *http.Request) {
	h.createBatchRequest(w, r, Resume)
}

func (h *Handler) revert(w http.ResponseWriter, r *http.Request) {
	h.createBatchRequest(w, r, Revert)
}

func (h *Handler) createBatchRequest(w http.ResponseWriter, r *http.Request, action Action) {
	jobID, ok := pathID(w, r)
	if !ok {
		return
	}
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	tx, err := h.platform.Begin(ctx)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal memulai transaksi")
		return
	}
	defer tx.Rollback(ctx)

	var databaseID, migrationID, revisionID int64
	var status, mode, executionID string
	err = tx.QueryRow(ctx, `
		SELECT j.db_connection_id, cr.migration_id, cr.revision_id, j.status, r.mode, j.execution_id::text
		FROM jobs j
		JOIN change_requests cr ON cr.id = j.change_request_id
		JOIN migration_revisions r ON r.id = cr.revision_id
		WHERE j.id = $1`, jobID,
	).Scan(&databaseID, &migrationID, &revisionID, &status, &mode, &executionID)
	if errors.Is(err, pgx.ErrNoRows) {
		web.WriteError(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca job")
		return
	}
	if !h.authorize(w, ctx, user.ID, databaseID, authz.Edit) {
		return
	}
	if mode != "batch" {
		web.WriteError(w, http.StatusConflict, "Resume dan revert hanya untuk migration batch")
		return
	}
	if status != "partial" {
		web.WriteError(w, http.StatusConflict, "Job tidak dalam status partial")
		return
	}
	if err := ensureNoOpenRequest(ctx, tx, migrationID, false); err != nil {
		writeRuleError(w, err)
		return
	}

	requestID, err := workflow.CreateRequest(ctx, tx, workflow.RequestInput{
		DatabaseID:        databaseID,
		MigrationID:       migrationID,
		RevisionID:        revisionID,
		Action:            string(action),
		TargetExecutionID: &executionID,
		RequestedBy:       user.ID,
	})
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membuat change request")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal menyimpan change request")
		return
	}

	obs.Logger(ctx).Info("change_request.created",
		"actor", user.Username, "database", databaseID, "status", "pending_review",
		"change_request_id", requestID, "migration_id", migrationID, "action", string(action),
		"target_execution_id", executionID)
	web.WriteJSON(w, http.StatusCreated, map[string]any{"changeRequestId": requestID, "status": "pending_review"})
}

func (h *Handler) listHistory(w http.ResponseWriter, r *http.Request) {
	databaseID, ok := pathID(w, r)
	if !ok {
		return
	}
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if !h.authorize(w, ctx, user.ID, databaseID, authz.View) {
		return
	}

	rows, err := h.platform.Query(ctx, `
		SELECT h.execution_id::text, h.attempt, h.migration_id, m.title, h.version, rv.revision_no,
		       h.action, h.status, h.reverts_execution_id::text, h.executed_sql, h.mode, h.batches,
		       h.rows_affected, h.error_message, h.sqlstate, u.username, h.started_at, h.finished_at
		FROM migration_history h
		JOIN migrations m ON m.id = h.migration_id
		JOIN migration_revisions rv ON rv.id = h.revision_id
		JOIN users u ON u.id = h.requested_by
		WHERE h.db_connection_id = $1
		ORDER BY h.finished_at, h.id`, databaseID)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca migration history")
		return
	}
	defer rows.Close()

	entries := make([]Entry, 0)
	for rows.Next() {
		var e Entry
		if err := rows.Scan(
			&e.ExecutionID, &e.Attempt, &e.MigrationID, &e.Title, &e.Version, &e.RevisionNo,
			&e.Action, &e.Status, &e.RevertsExecutionID, &e.ExecutedSQL, &e.Mode, &e.Batches,
			&e.RowsAffected, &e.ErrorMessage, &e.SQLState, &e.RequestedBy, &e.StartedAt, &e.FinishedAt,
		); err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal membaca migration history")
			return
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca migration history")
		return
	}

	if r.URL.Query().Get("hideRollback") == "true" {
		entries = HideRollback(entries)
	}
	web.WriteJSON(w, http.StatusOK, entries)
}

const jobSelect = `
	SELECT j.id, j.change_request_id, j.db_connection_id, cr.migration_id, m.title, cr.action, rv.mode,
	       j.execution_id::text, j.status, j.attempt, j.max_attempts, j.batches_committed,
	       j.rows_committed, j.last_error, j.last_sqlstate, j.created_at, j.started_at, j.finished_at,
	       cr.target_execution_id::text
	FROM jobs j
	JOIN change_requests cr ON cr.id = j.change_request_id
	JOIN migrations m ON m.id = cr.migration_id
	JOIN migration_revisions rv ON rv.id = cr.revision_id
`

func scanJob(row pgx.Row) (Job, error) {
	var j Job
	err := row.Scan(
		&j.ID, &j.ChangeRequestID, &j.DatabaseID, &j.MigrationID, &j.MigrationTitle, &j.Action, &j.Mode,
		&j.ExecutionID, &j.Status, &j.Attempt, &j.MaxAttempts, &j.BatchesCommitted,
		&j.RowsCommitted, &j.LastError, &j.LastSQLState, &j.CreatedAt, &j.StartedAt, &j.FinishedAt,
		&j.target,
	)
	return j, err
}

func jobOrigin(ctx context.Context, q store.Querier, action Action, target *string) (Action, Direction, error) {
	switch action {
	case Apply, Forward:
		return action, Up, nil
	case Rollback:
		return Rollback, Down, nil
	case Resume, Revert:
		if target == nil {
			return "", "", fmt.Errorf("job %s tidak punya execution ID tujuan", action)
		}
		o, err := traceOrigin(ctx, q, *target, 0)
		if err != nil {
			return "", "", err
		}
		if action == Resume {
			return o.action, o.direction, nil
		}
		return Revert, opposite(o.direction), nil
	default:
		return "", "", fmt.Errorf("aksi %q tidak dikenal", action)
	}
}

func (h *Handler) fillOrigin(ctx context.Context, j *Job) error {
	action, direction, err := jobOrigin(ctx, h.platform, Action(j.Action), j.target)
	if err != nil {
		return err
	}
	j.EffectiveAction, j.Direction = string(action), string(direction)
	return nil
}

func (h *Handler) getJob(w http.ResponseWriter, r *http.Request) {
	jobID, ok := pathID(w, r)
	if !ok {
		return
	}
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	job, err := scanJob(h.platform.QueryRow(ctx, jobSelect+` WHERE j.id = $1`, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		web.WriteError(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca job")
		return
	}
	if !h.authorize(w, ctx, user.ID, job.DatabaseID, authz.View) {
		return
	}
	if err := h.fillOrigin(ctx, &job); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca job")
		return
	}
	web.WriteJSON(w, http.StatusOK, job)
}

func (h *Handler) listJobs(w http.ResponseWriter, r *http.Request) {
	databaseID, ok := pathID(w, r)
	if !ok {
		return
	}
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if !h.authorize(w, ctx, user.ID, databaseID, authz.View) {
		return
	}

	rows, err := h.platform.Query(ctx, jobSelect+`
		WHERE j.db_connection_id = $1
		ORDER BY j.created_at DESC, j.id DESC
		LIMIT 100`, databaseID)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca daftar job")
		return
	}
	defer rows.Close()

	jobs := make([]Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal membaca daftar job")
			return
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca daftar job")
		return
	}
	rows.Close()
	for i := range jobs {
		if err := h.fillOrigin(ctx, &jobs[i]); err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal membaca daftar job")
			return
		}
	}
	web.WriteJSON(w, http.StatusOK, jobs)
}

func ensureNoOpenRequest(ctx context.Context, q store.Querier, migrationID int64, includeUnfinished bool) error {
	statuses := []string{"pending_review", "approved", "queued", "running"}
	if includeUnfinished {
		statuses = append(statuses, "partial", "uncertain")
	}
	var open bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM change_requests WHERE migration_id = $1 AND status = ANY($2)
		)`, migrationID, statuses,
	).Scan(&open)
	if err != nil {
		return err
	}
	if open {
		return errOpenRequest
	}
	return nil
}

func writeRuleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrMigrationNotFound):
		web.WriteError(w, http.StatusNotFound, "Migration tidak ditemukan")
	case errors.Is(err, ErrNotApplied), errors.Is(err, ErrNotTop), errors.Is(err, ErrIrreversible),
		errors.Is(err, ErrAlreadyApplied), errors.Is(err, ErrNeverApplied),
		errors.Is(err, ErrNotNextForward), errors.Is(err, errOpenRequest):
		web.WriteError(w, http.StatusConflict, capitalize(err.Error()))
	default:
		web.WriteError(w, http.StatusInternalServerError, "Gagal memeriksa posisi migration")
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (h *Handler) authorize(w http.ResponseWriter, ctx context.Context, userID, databaseID int64, level authz.Level) bool {
	err := h.authorizer.Require(ctx, userID, databaseID, level)
	if errors.Is(err, authz.ErrForbidden) {
		web.WriteError(w, http.StatusForbidden, "Tidak punya akses ke database ini")
		return false
	}
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal memeriksa akses database")
		return false
	}
	return true
}

func requireUser(w http.ResponseWriter, r *http.Request) (*auth.User, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		web.WriteError(w, http.StatusUnauthorized, "Belum login")
		return nil, false
	}
	return user, true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		web.WriteError(w, http.StatusBadRequest, "ID tidak valid")
		return 0, false
	}
	return id, true
}
