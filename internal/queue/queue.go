package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotApproved   = errors.New("change request belum approved")
	ErrAlreadyQueued = errors.New("change request sudah pernah diantrekan")
)

func Enqueue(ctx context.Context, tx pgx.Tx, changeRequestID int64, trace map[string]string) (int64, string, error) {
	traceJSON, err := json.Marshal(trace)
	if err != nil {
		return 0, "", fmt.Errorf("encode trace context: %w", err)
	}

	var jobID int64
	var executionID string
	err = tx.QueryRow(ctx, `
		INSERT INTO jobs (change_request_id, db_connection_id, execution_id, status, trace_context)
		SELECT id, db_connection_id, gen_random_uuid(), 'queued', $2
		FROM change_requests
		WHERE id = $1 AND status = 'approved'
		RETURNING id, execution_id::text`,
		changeRequestID, traceJSON,
	).Scan(&jobID, &executionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrNotApproved
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return 0, "", ErrAlreadyQueued
	}
	if err != nil {
		return 0, "", fmt.Errorf("buat job: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE change_requests SET status = 'queued', updated_at = now() WHERE id = $1`,
		changeRequestID,
	); err != nil {
		return 0, "", fmt.Errorf("update status change request: %w", err)
	}
	return jobID, executionID, nil
}
