package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/fahdmz/K03-05-WBD/internal/history"
	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/jackc/pgx/v5"
)

type rootInfo struct {
	exec   string         // root: semua checkpoint & bukti di target pake ID ini
	action history.Action // apply / forward / rollback
	target *string        // root rollback: root eksekusi yang dibatalin (sumber checkpoint up)
}

// findRoot nentuin root checkpoint buat job execID dengan plan p.
// History tetep pake execution ID job masing-masing, cuma bukti di target yang pake root.
func findRoot(ctx context.Context, q store.Querier, execID string, p history.Plan) (rootInfo, error) {
	switch p.Action {
	case history.Apply, history.Forward:
		return rootInfo{exec: execID, action: p.Action}, nil
	case history.Rollback:
		return rootInfo{exec: execID, action: history.Rollback, target: p.CheckpointExecutionID}, nil
	case history.Resume, history.Revert:
		if p.CheckpointExecutionID == nil {
			return rootInfo{}, fmt.Errorf("%s butuh eksekusi asal", p.Action)
		}
		ri := rootInfo{exec: *p.CheckpointExecutionID, action: history.Apply}
		var action string
		var target *string
		err := q.QueryRow(ctx, `
			SELECT cr.action, cr.target_execution_id::text
			FROM jobs j JOIN change_requests cr ON cr.id = j.change_request_id
			WHERE j.execution_id = $1`, ri.exec,
		).Scan(&action, &target)
		if errors.Is(err, pgx.ErrNoRows) {
			return ri, nil // eksekusi lama (konversi M1) dianggap apply
		}
		if err != nil {
			return rootInfo{}, fmt.Errorf("baca eksekusi asal: %w", err)
		}
		ri.action = history.Action(action)
		if ri.action == history.Rollback && target != nil {
			src, err := chainRoot(ctx, q, *target)
			if err != nil {
				return rootInfo{}, err
			}
			ri.target = &src
		}
		return ri, nil
	}
	return rootInfo{}, fmt.Errorf("aksi %q tidak dikenal", p.Action)
}

// chainRoot mundur lewat resume/revert sampe ketemu apply/forward/rollback
func chainRoot(ctx context.Context, q store.Querier, execID string) (string, error) {
	var root string
	err := q.QueryRow(ctx, `
		WITH RECURSIVE up AS (
			SELECT j.execution_id::text AS exec_id, cr.action, cr.target_execution_id::text AS target, 0 AS depth
			FROM jobs j JOIN change_requests cr ON cr.id = j.change_request_id
			WHERE j.execution_id = $1
			UNION ALL
			SELECT j.execution_id::text, cr.action, cr.target_execution_id::text, up.depth + 1
			FROM up
			JOIN jobs j ON j.execution_id::text = up.target
			JOIN change_requests cr ON cr.id = j.change_request_id
			WHERE up.action IN ('resume', 'revert') AND up.depth < 100
		)
		SELECT exec_id FROM up ORDER BY depth DESC LIMIT 1`, execID,
	).Scan(&root)
	if errors.Is(err, pgx.ErrNoRows) {
		return execID, nil
	}
	if err != nil {
		return "", fmt.Errorf("cari root eksekusi: %w", err)
	}
	return root, nil
}
