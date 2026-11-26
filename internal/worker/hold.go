package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// famCTE: keluarga eksekusi job $1 = root-nya (mundur lewat target_execution_id sampe
// apply/forward/rollback) plus semua resume/revert turunan root itu, lengkap sama arahnya.
const famCTE = `
	WITH RECURSIVE up AS (
		SELECT j.execution_id AS exec_id, cr.action, cr.target_execution_id AS target, 0 AS depth
		FROM jobs j JOIN change_requests cr ON cr.id = j.change_request_id
		WHERE j.id = $1
		UNION ALL
		SELECT j.execution_id, cr.action, cr.target_execution_id, up.depth + 1
		FROM up
		JOIN jobs j ON j.execution_id = up.target
		JOIN change_requests cr ON cr.id = j.change_request_id
		WHERE up.action IN ('resume', 'revert') AND up.depth < 100
	),
	root AS (
		SELECT exec_id, action FROM up ORDER BY depth DESC LIMIT 1
	),
	fam AS (
		SELECT j.id, j.execution_id AS exec_id, j.status,
			CASE WHEN root.action = 'rollback' THEN 'down' ELSE 'up' END AS dir, 0 AS depth
		FROM root JOIN jobs j ON j.execution_id = root.exec_id
		UNION ALL
		SELECT j.id, j.execution_id, j.status,
			CASE WHEN cr.action = 'revert' THEN (CASE WHEN fam.dir = 'up' THEN 'down' ELSE 'up' END)
				ELSE fam.dir END,
			fam.depth + 1
		FROM fam
		JOIN change_requests cr ON cr.target_execution_id = fam.exec_id AND cr.action IN ('resume', 'revert')
		JOIN jobs j ON j.change_request_id = cr.id
		WHERE fam.depth < 100
	)`

// heldBy nyari job lain di DB yang sama yang hasilnya belum beres: partial, uncertain,
// running yang worker-nya mati, atau retry batch yang udah punya batch committed.
// Job yang belum pernah nyentuh target (attempt 0) dan satu keluarga eksekusi nggak dihitung.
func (w *Worker) heldBy(ctx context.Context, j job) (int64, string, error) {
	var id int64
	var status string
	err := w.platform.QueryRow(ctx, famCTE+`
		SELECT j.id, j.status FROM jobs j
		WHERE j.db_connection_id = $2 AND j.id <> $1
		  AND j.execution_id NOT IN (SELECT exec_id FROM fam)
		  AND (j.status = 'partial'
		       OR (j.attempt > 0 AND (
		              j.status = 'uncertain'
		           OR (j.status = 'running' AND j.lease_until < now())
		           OR (j.status = 'queued' AND j.batches_committed > 0))))
		ORDER BY j.id
		LIMIT 1`,
		j.ID, j.DBID,
	).Scan(&id, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("cek job yang belum beres: %w", err)
	}
	return id, status, nil
}
