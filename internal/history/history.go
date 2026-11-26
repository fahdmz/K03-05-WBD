package history

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/jackc/pgx/v5"
)

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

type Status string

const (
	Succeeded Status = "success"
	Failed    Status = "failed"
	Partial   Status = "partial"
)

type Plan struct {
	ChangeRequestID       int64
	DatabaseID            int64
	MigrationID           int64
	RevisionID            int64
	RequestedBy           int64
	Action                Action
	Direction             Direction
	Mode                  string
	SQL                   string
	Batch                 *Batch
	TargetExecutionID     *string
	RevertsExecutionID    *string
	CheckpointExecutionID *string
}

type Result struct {
	Plan         Plan
	ExecutionID  string
	Attempt      int
	Status       Status
	ExecutedSQL  string
	Batches      int
	RowsAffected int64
	ErrorMessage string
	SQLState     string
	StartedAt    time.Time
	FinishedAt   time.Time
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

	useDirection := func(d Direction) error {
		if d == Up {
			p.Direction = Up
			return nil
		}
		if sqlDown == nil {
			return ErrIrreversible
		}
		p.Direction = Down
		p.SQL = *sqlDown
		return nil
	}

	switch p.Action {
	case Apply:
		p.Direction = Up
	case Rollback:
		t, err := CheckRollback(ctx, q, p.MigrationID)
		if err != nil {
			return Plan{}, err
		}
		if p.TargetExecutionID == nil || *p.TargetExecutionID != t.ExecutionID {
			return Plan{}, ErrStale
		}
		o, err := traceOrigin(ctx, q, *p.TargetExecutionID, 0)
		if err != nil {
			return Plan{}, err
		}
		p.CheckpointExecutionID = &o.root
		if err := useDirection(Down); err != nil {
			return Plan{}, err
		}
		p.RevertsExecutionID = p.TargetExecutionID
	case Forward:
		if _, err := CheckForward(ctx, q, p.MigrationID); err != nil {
			return Plan{}, err
		}
		p.Direction = Up
	case Resume, Revert:
		if p.TargetExecutionID == nil {
			return Plan{}, fmt.Errorf("%s butuh execution ID tujuan", p.Action)
		}
		o, err := traceOrigin(ctx, q, *p.TargetExecutionID, 0)
		if err != nil {
			return Plan{}, err
		}
		direction := o.direction
		if p.Action == Revert {
			direction = opposite(direction)
		}
		if err := useDirection(direction); err != nil {
			return Plan{}, err
		}
		if p.Action == Resume && o.action == Rollback {
			p.RevertsExecutionID = o.target
		}
		p.CheckpointExecutionID = &o.root
	default:
		return Plan{}, fmt.Errorf("aksi %q tidak dikenal", p.Action)
	}
	return p, nil
}

func Record(ctx context.Context, tx pgx.Tx, r Result) error {
	switch r.Status {
	case Succeeded, Failed, Partial:
	default:
		return fmt.Errorf("status %q tidak dikenal", r.Status)
	}
	if r.Attempt < 1 {
		return fmt.Errorf("attempt harus minimal 1, didapat %d", r.Attempt)
	}

	action := r.Plan.Action
	reverts := r.Plan.RevertsExecutionID
	if action == Resume {
		if r.Plan.TargetExecutionID == nil {
			return errors.New("resume butuh execution ID tujuan")
		}
		o, err := traceOrigin(ctx, tx, *r.Plan.TargetExecutionID, 0)
		if err != nil {
			return err
		}
		action = o.action
		if o.action == Rollback {
			reverts = o.target
		}
	}

	var version *int
	err := tx.QueryRow(ctx, `SELECT version FROM migrations WHERE id = $1 FOR UPDATE`, r.Plan.MigrationID).Scan(&version)
	if err != nil {
		return fmt.Errorf("kunci migration %d: %w", r.Plan.MigrationID, err)
	}

	if r.Status == Succeeded {
		version, err = applyPosition(ctx, tx, r.Plan, action, version)
		if err != nil {
			return err
		}
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO migration_history (
			execution_id, attempt, db_connection_id, migration_id, revision_id, version, action, status,
			reverts_execution_id, executed_sql, mode, batches, rows_affected, error_message, sqlstate,
			requested_by, started_at, finished_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
		r.ExecutionID, r.Attempt, r.Plan.DatabaseID, r.Plan.MigrationID, r.Plan.RevisionID, version,
		string(action), string(r.Status), reverts, r.ExecutedSQL, r.Plan.Mode,
		r.Batches, r.RowsAffected, nullIfEmpty(r.ErrorMessage), nullIfEmpty(r.SQLState),
		r.Plan.RequestedBy, r.StartedAt, r.FinishedAt,
	)
	if err != nil {
		return fmt.Errorf("catat migration history: %w", err)
	}
	return nil
}

func applyPosition(ctx context.Context, tx pgx.Tx, plan Plan, action Action, version *int) (*int, error) {
	applied := true
	switch action {
	case Apply:
		if version == nil {
			var next int
			err := tx.QueryRow(ctx, `
				SELECT COALESCE(MAX(version), 0) + 1 FROM migrations WHERE db_connection_id = $1`,
				plan.DatabaseID,
			).Scan(&next)
			if err != nil {
				return nil, fmt.Errorf("hitung versi baru: %w", err)
			}
			version = &next
		}
	case Forward:
		if version == nil {
			return nil, ErrNeverApplied
		}
	case Rollback:
		if version == nil {
			return nil, ErrNotApplied
		}
		applied = false
	case Revert:
		return version, nil
	default:
		return nil, fmt.Errorf("aksi %q tidak dicatat di history", action)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE migrations SET version = $2, is_applied = $3 WHERE id = $1`,
		plan.MigrationID, *version, applied,
	); err != nil {
		return nil, fmt.Errorf("update posisi migration: %w", err)
	}
	return version, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
