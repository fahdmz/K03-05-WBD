package history

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/jackc/pgx/v5"
)

var ErrNotImplemented = errors.New("history: belum diimplementasikan")

type Action string

const (
	Apply    Action = "apply"
	Rollback Action = "rollback"
	Forward  Action = "forward"
	Resume   Action = "resume"
	Revert   Action = "revert"
)

type Direction string

const (
	Up   Direction = "up"
	Down Direction = "down"
)

type Batch struct {
	Table  string
	Column string
	Size   int
}

type Plan struct {
	ChangeRequestID    int64
	DatabaseID         int64
	MigrationID        int64
	RevisionID         int64
	RequestedBy        int64
	Action             Action
	Direction          Direction
	Mode               string
	SQL                string
	Batch              *Batch
	TargetExecutionID  *string
	RevertsExecutionID *string
}

type Result struct {
	Plan         Plan
	ExecutionID  string
	ExecutedSQL  string
	Batches      int
	RowsAffected int64
	StartedAt    time.Time
	CommittedAt  time.Time
}

func BuildPlan(ctx context.Context, q store.Querier, changeRequestID int64) (Plan, error) {
	var p Plan
	var action string
	var sqlDown, batchTable, batchColumn *string
	var batchSize *int
	err := q.QueryRow(ctx, `
		SELECT cr.id, cr.db_connection_id, cr.migration_id, cr.revision_id, cr.requested_by,
		       cr.action, cr.target_execution_id::text,
		       r.sql_up, r.sql_down, r.mode, r.batch_table, r.batch_column, r.batch_size
		FROM change_requests cr
		JOIN migration_revisions r ON r.id = cr.revision_id
		WHERE cr.id = $1`, changeRequestID,
	).Scan(
		&p.ChangeRequestID, &p.DatabaseID, &p.MigrationID, &p.RevisionID, &p.RequestedBy,
		&action, &p.TargetExecutionID,
		&p.SQL, &sqlDown, &p.Mode, &batchTable, &batchColumn, &batchSize,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, fmt.Errorf("change request %d tidak ditemukan", changeRequestID)
	}
	if err != nil {
		return Plan{}, fmt.Errorf("baca change request %d: %w", changeRequestID, err)
	}

	p.Action = Action(action)
	if p.Mode == "batch" && batchTable != nil && batchColumn != nil && batchSize != nil {
		p.Batch = &Batch{Table: *batchTable, Column: *batchColumn, Size: *batchSize}
	}

	switch p.Action {
	case Apply:
		p.Direction = Up
		return p, nil
	default:
		return Plan{}, ErrNotImplemented
	}
}

func Record(ctx context.Context, tx pgx.Tx, r Result) error {
	if r.Plan.Action != Apply {
		return ErrNotImplemented
	}

	var version *int
	err := tx.QueryRow(ctx, `SELECT version FROM migrations WHERE id = $1 FOR UPDATE`, r.Plan.MigrationID).Scan(&version)
	if err != nil {
		return fmt.Errorf("kunci migration %d: %w", r.Plan.MigrationID, err)
	}
	if version == nil {
		var next int
		err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(version), 0) + 1 FROM migrations WHERE db_connection_id = $1`,
			r.Plan.DatabaseID,
		).Scan(&next)
		if err != nil {
			return fmt.Errorf("hitung versi baru: %w", err)
		}
		version = &next
	}

	if _, err := tx.Exec(ctx, `
		UPDATE migrations SET version = $2, is_applied = TRUE WHERE id = $1`,
		r.Plan.MigrationID, *version,
	); err != nil {
		return fmt.Errorf("update posisi migration: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO migration_history (
			execution_id, db_connection_id, migration_id, revision_id, version, action,
			reverts_execution_id, executed_sql, mode, batches, rows_affected,
			requested_by, started_at, committed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		r.ExecutionID, r.Plan.DatabaseID, r.Plan.MigrationID, r.Plan.RevisionID, *version,
		string(r.Plan.Action), r.Plan.RevertsExecutionID, r.ExecutedSQL, r.Plan.Mode,
		r.Batches, r.RowsAffected, r.Plan.RequestedBy, r.StartedAt, r.CommittedAt,
	)
	if err != nil {
		return fmt.Errorf("catat migration history: %w", err)
	}
	return nil
}
