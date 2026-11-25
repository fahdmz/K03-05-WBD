package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/jackc/pgx/v5"
)

type Claimed struct {
	ID          int64
	CRID        int64
	DBID        int64
	ExecID      string
	PrevStatus  string
	Attempt     int
	MaxAttempts int
	Trace       map[string]string
	First       bool    // claim pertama (belum pernah di-claim)
	Progress    int     // batch yang udah committed (dari attempt sebelumnya)
	Waited      float64 // detik sejak job dibikin
}

// Claim ngambil satu job yang siap jalan. nil, nil kalau antrean kosong.
// running yang lease-nya habis = worker sebelumnya mati, ikut diambil (recovery).
func Claim(ctx context.Context, q store.Querier, workerID string, lease time.Duration) (*Claimed, error) {
	var c Claimed
	var trace []byte
	err := q.QueryRow(ctx, `
		WITH c AS (
			SELECT id, status, started_at IS NULL AS first FROM jobs
			WHERE (status IN ('queued', 'uncertain') AND run_after <= now())
			   OR (status = 'running' AND lease_until < now())
			ORDER BY run_after, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE jobs j SET status = 'running', worker_id = $1,
			lease_until = now() + make_interval(secs => $2),
			started_at = COALESCE(j.started_at, now())
		FROM c WHERE j.id = c.id
		RETURNING j.id, j.change_request_id, j.db_connection_id, j.execution_id::text,
			c.status, j.attempt, j.max_attempts, j.trace_context, c.first, j.batches_committed,
			GREATEST(extract(epoch FROM now() - j.created_at), 0)::float8`,
		workerID, lease.Seconds(),
	).Scan(&c.ID, &c.CRID, &c.DBID, &c.ExecID, &c.PrevStatus, &c.Attempt, &c.MaxAttempts, &trace, &c.First, &c.Progress, &c.Waited)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	if len(trace) > 0 {
		if err := json.Unmarshal(trace, &c.Trace); err != nil {
			c.Trace = nil
		}
	}
	return &c, nil
}

// Requeue balikin job ke antrean tanpa nambah attempt (lock sibuk / ketahan).
// uncertain = job udah pernah nyentuh target dan hasilnya belum dicek, jadi tetep uncertain.
func Requeue(ctx context.Context, q store.Querier, jobID int64, workerID string, uncertain bool, delay time.Duration) error {
	status := "queued"
	if uncertain {
		status = "uncertain"
	}
	_, err := q.Exec(ctx, `
		WITH j AS (
			UPDATE jobs SET status = $3, run_after = now() + make_interval(secs => $4), lease_until = NULL
			WHERE id = $1 AND worker_id = $2 AND status = 'running'
			RETURNING change_request_id
		)
		UPDATE change_requests cr SET status = $3, updated_at = now()
		FROM j WHERE cr.id = j.change_request_id`,
		jobID, workerID, status, delay.Seconds(),
	)
	if err != nil {
		return fmt.Errorf("balikin job %d ke antrean: %w", jobID, err)
	}
	return nil
}
