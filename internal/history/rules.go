package history

import (
	"context"
	"errors"
	"fmt"

	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/jackc/pgx/v5"
)

var (
	ErrMigrationNotFound = errors.New("migration tidak ditemukan")
	ErrNotApplied        = errors.New("migration tidak sedang aktif")
	ErrNotTop            = errors.New("hanya migration aktif terakhir yang bisa di-rollback")
	ErrIrreversible      = errors.New("migration ini irreversible sehingga tidak bisa di-rollback")
	ErrAlreadyApplied    = errors.New("migration sudah aktif")
	ErrNeverApplied      = errors.New("migration belum pernah diterapkan")
	ErrNotNextForward    = errors.New("forward hanya bisa untuk migration tepat setelah posisi aktif")
	ErrStale             = errors.New("posisi migration sudah berubah sejak request dibuat")
)

type Target struct {
	DatabaseID  int64
	MigrationID int64
	RevisionID  int64
	ExecutionID string
}

type migrationState struct {
	databaseID int64
	version    *int
	isApplied  bool
}

func loadState(ctx context.Context, q store.Querier, migrationID int64) (migrationState, error) {
	var s migrationState
	err := q.QueryRow(ctx, `
		SELECT db_connection_id, version, is_applied FROM migrations WHERE id = $1`,
		migrationID,
	).Scan(&s.databaseID, &s.version, &s.isApplied)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrMigrationNotFound
	}
	if err != nil {
		return s, fmt.Errorf("baca migration %d: %w", migrationID, err)
	}
	return s, nil
}

func activeTop(ctx context.Context, q store.Querier, databaseID int64) (int, error) {
	var top int
	err := q.QueryRow(ctx, `
		SELECT COALESCE(MAX(version), 0) FROM migrations
		WHERE db_connection_id = $1 AND is_applied`,
		databaseID,
	).Scan(&top)
	if err != nil {
		return 0, fmt.Errorf("baca posisi aktif: %w", err)
	}
	return top, nil
}

func CheckRollback(ctx context.Context, q store.Querier, migrationID int64) (Target, error) {
	s, err := loadState(ctx, q, migrationID)
	if err != nil {
		return Target{}, err
	}
	if !s.isApplied || s.version == nil {
		return Target{}, ErrNotApplied
	}
	top, err := activeTop(ctx, q, s.databaseID)
	if err != nil {
		return Target{}, err
	}
	if *s.version != top {
		return Target{}, ErrNotTop
	}

	t := Target{DatabaseID: s.databaseID, MigrationID: migrationID}
	var irreversible bool
	err = q.QueryRow(ctx, `
		SELECT h.execution_id::text, h.revision_id, r.irreversible
		FROM migration_history h
		JOIN migration_revisions r ON r.id = h.revision_id
		WHERE h.migration_id = $1
		  AND h.action IN ('apply', 'forward')
		  AND NOT EXISTS (
		      SELECT 1 FROM migration_history x WHERE x.reverts_execution_id = h.execution_id
		  )
		ORDER BY h.committed_at DESC, h.id DESC
		LIMIT 1`,
		migrationID,
	).Scan(&t.ExecutionID, &t.RevisionID, &irreversible)
	if errors.Is(err, pgx.ErrNoRows) {
		return Target{}, ErrNotApplied
	}
	if err != nil {
		return Target{}, fmt.Errorf("baca eksekusi aktif: %w", err)
	}
	if irreversible {
		return Target{}, ErrIrreversible
	}
	return t, nil
}

func CheckForward(ctx context.Context, q store.Querier, migrationID int64) (Target, error) {
	s, err := loadState(ctx, q, migrationID)
	if err != nil {
		return Target{}, err
	}
	if s.isApplied {
		return Target{}, ErrAlreadyApplied
	}
	if s.version == nil {
		return Target{}, ErrNeverApplied
	}
	top, err := activeTop(ctx, q, s.databaseID)
	if err != nil {
		return Target{}, err
	}

	var next *int
	err = q.QueryRow(ctx, `
		SELECT MIN(version) FROM migrations
		WHERE db_connection_id = $1 AND version > $2`,
		s.databaseID, top,
	).Scan(&next)
	if err != nil {
		return Target{}, fmt.Errorf("cari migration berikutnya: %w", err)
	}
	if next == nil || *next != *s.version {
		return Target{}, ErrNotNextForward
	}

	t := Target{DatabaseID: s.databaseID, MigrationID: migrationID}
	err = q.QueryRow(ctx, `
		SELECT revision_id FROM migration_history
		WHERE migration_id = $1 AND action IN ('apply', 'forward')
		ORDER BY committed_at DESC, id DESC
		LIMIT 1`,
		migrationID,
	).Scan(&t.RevisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Target{}, ErrNeverApplied
	}
	if err != nil {
		return Target{}, fmt.Errorf("baca revisi terakhir: %w", err)
	}
	return t, nil
}

func originalAction(ctx context.Context, q store.Querier, executionID string) (Action, *string, error) {
	var action string
	var target *string
	err := q.QueryRow(ctx, `
		SELECT cr.action, cr.target_execution_id::text
		FROM jobs j
		JOIN change_requests cr ON cr.id = j.change_request_id
		WHERE j.execution_id = $1`,
		executionID,
	).Scan(&action, &target)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, fmt.Errorf("eksekusi %s tidak ditemukan", executionID)
	}
	if err != nil {
		return "", nil, fmt.Errorf("baca eksekusi %s: %w", executionID, err)
	}
	return Action(action), target, nil
}
