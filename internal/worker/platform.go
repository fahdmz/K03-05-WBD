package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/fahdmz/K03-05-WBD/internal/store"
)

// schema bukti eksekusi di database target, dibikin sekali (IF NOT EXISTS)
const platformDDL = `
CREATE SCHEMA IF NOT EXISTS _platform;
CREATE TABLE IF NOT EXISTS _platform.executions (
    execution_id UUID NOT NULL,
    attempt INT NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('up', 'down')),
    committed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (execution_id, direction)
);
CREATE TABLE IF NOT EXISTS _platform.batch_checkpoints (
    execution_id UUID NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('up', 'down')),
    batch_no INT NOT NULL,
    lo BIGINT NOT NULL,
    hi BIGINT NOT NULL,
    rows_affected BIGINT NOT NULL,
    committed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (execution_id, direction, batch_no)
);
CREATE TABLE IF NOT EXISTS _platform.batch_scopes (
    execution_id UUID NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('up', 'down')),
    max_value BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (execution_id, direction)
);`

func ensurePlatform(ctx context.Context, target store.Querier) error {
	if _, err := target.Exec(ctx, platformDDL); err != nil {
		return fmt.Errorf("siapin schema _platform: %w", err)
	}
	return nil
}

// executed: udah ada bukti commit buat root+arah ini?
func executed(ctx context.Context, q store.Querier, root, dir string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM _platform.executions WHERE execution_id = $1 AND direction = $2
		)`, root, dir,
	).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("cek bukti eksekusi: %w", err)
	}
	return ok, nil
}

func markExecuted(ctx context.Context, q store.Querier, root string, attempt int, dir string) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO _platform.executions (execution_id, attempt, direction) VALUES ($1, $2, $3)`,
		root, attempt, dir,
	); err != nil {
		return &evidenceErr{fmt.Errorf("catat bukti eksekusi: %w", err)}
	}
	return nil
}

// evidenceErr: error dari nulis bukti/checkpoint di _platform (bukan dari SQL user).
// 23505 di sini artinya eksekutor lain udah nulis duluan.
type evidenceErr struct{ err error }

func (e *evidenceErr) Error() string { return e.err.Error() }
func (e *evidenceErr) Unwrap() error { return e.err }

func isEvidenceErr(err error) bool {
	var e *evidenceErr
	return errors.As(err, &e)
}
