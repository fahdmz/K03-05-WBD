package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/jackc/pgx/v5"
)

var ErrNotImplemented = errors.New("workflow: belum diimplementasikan")

type RequestInput struct {
	DatabaseID        int64
	MigrationID       int64
	RevisionID        int64
	Action            string
	TargetExecutionID *string
	RequestedBy       int64
}

type GroupApproval struct {
	GroupID   int64
	GroupName string
	LeaderID  *int64
	Decision  *string
}

type Status struct {
	Approved bool
	Rejected bool
	Groups   []GroupApproval
}

func CreateRequest(ctx context.Context, tx pgx.Tx, in RequestInput) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO change_requests (
			db_connection_id, migration_id, revision_id, action, target_execution_id,
			status, requested_by
		) VALUES ($1, $2, $3, $4, $5, 'pending_review', $6)
		RETURNING id`,
		in.DatabaseID, in.MigrationID, in.RevisionID, in.Action, in.TargetExecutionID, in.RequestedBy,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("buat change request: %w", err)
	}
	return id, nil
}

func ApprovalStatus(ctx context.Context, q store.Querier, changeRequestID int64) (Status, error) {
	return Status{}, ErrNotImplemented
}

func Revalidate(ctx context.Context, tx pgx.Tx, changeRequestID int64) error {
	return nil
}
