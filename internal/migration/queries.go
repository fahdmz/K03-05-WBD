package migration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/fahdmz/K03-05-WBD/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const migrationSelect = `
	SELECT
		m.id, m.db_connection_id, m.version, m.is_applied, m.title,
		creator.username, m.created_at,
		r.id, r.migration_id, r.revision_no, r.status,
		r.sql_up, r.sql_down, r.irreversible, r.irreversible_reason,
		r.mode, r.batch_table, r.batch_column, r.batch_size,
		rev_creator.username, r.created_at, r.submitted_at,
		(
			SELECT cr.status
			FROM change_requests cr
			WHERE cr.migration_id = m.id
			ORDER BY cr.created_at DESC, cr.id DESC
			LIMIT 1
		) AS latest_request_status
	FROM migrations m
	JOIN users creator ON creator.id = m.created_by
	LEFT JOIN migration_revisions r ON r.id = m.current_revision_id
	LEFT JOIN users rev_creator ON rev_creator.id = r.created_by
`

func (h *Handler) loadCurrentRevision(ctx context.Context, q store.Querier, migrationID int64) (Revision, error) {
	row := q.QueryRow(ctx, `
		SELECT
			r.id, r.migration_id, r.revision_no, r.status,
			r.sql_up, r.sql_down, r.irreversible, r.irreversible_reason,
			r.mode, r.batch_table, r.batch_column, r.batch_size,
			u.username, r.created_at, r.submitted_at
		FROM migrations m
		JOIN migration_revisions r ON r.id = m.current_revision_id
		JOIN users u ON u.id = r.created_by
		WHERE m.id = $1
	`, migrationID)
	rev, err := scanRevision(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Revision{}, errMigrationNotFound
	}
	return rev, err
}

func (h *Handler) loadMigrationDetail(ctx context.Context, q store.Querier, id int64) (MigrationDetail, error) {
	summary, err := scanMigrationSummary(q.QueryRow(ctx, migrationSelect+` WHERE m.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return MigrationDetail{}, errMigrationNotFound
	}
	if err != nil {
		return MigrationDetail{}, err
	}

	revRows, err := q.Query(ctx, `
		SELECT
			r.id, r.migration_id, r.revision_no, r.status,
			r.sql_up, r.sql_down, r.irreversible, r.irreversible_reason,
			r.mode, r.batch_table, r.batch_column, r.batch_size,
			u.username, r.created_at, r.submitted_at
		FROM migration_revisions r
		JOIN users u ON u.id = r.created_by
		WHERE r.migration_id = $1
		ORDER BY r.revision_no ASC
	`, id)
	if err != nil {
		return MigrationDetail{}, err
	}
	defer revRows.Close()

	revisions := make([]Revision, 0)
	for revRows.Next() {
		rev, err := scanRevision(revRows)
		if err != nil {
			return MigrationDetail{}, err
		}
		revisions = append(revisions, rev)
	}
	if err := revRows.Err(); err != nil {
		return MigrationDetail{}, err
	}

	crRows, err := q.Query(ctx, `
		SELECT
			cr.id, cr.revision_id, cr.action, cr.target_execution_id::text,
			cr.status, cr.validation, u.username, cr.created_at, cr.updated_at
		FROM change_requests cr
		JOIN users u ON u.id = cr.requested_by
		WHERE cr.migration_id = $1
		ORDER BY cr.created_at DESC, cr.id DESC
	`, id)
	if err != nil {
		return MigrationDetail{}, err
	}
	defer crRows.Close()

	requests := make([]ChangeRequestSummary, 0)
	for crRows.Next() {
		var cr ChangeRequestSummary
		var targetExec pgtype.Text
		var valBytes []byte
		if err := crRows.Scan(
			&cr.ID, &cr.RevisionID, &cr.Action, &targetExec,
			&cr.Status, &valBytes, &cr.RequestedBy, &cr.CreatedAt, &cr.UpdatedAt,
		); err != nil {
			return MigrationDetail{}, err
		}
		if targetExec.Valid {
			cr.TargetExecutionID = &targetExec.String
		}
		if len(valBytes) > 0 {
			cr.Validation = json.RawMessage(valBytes)
		}
		requests = append(requests, cr)
	}
	if err := crRows.Err(); err != nil {
		return MigrationDetail{}, err
	}

	return MigrationDetail{
		MigrationSummary: summary,
		Revisions:        revisions,
		ChangeRequests:   requests,
	}, nil
}

func scanMigrationSummary(row rowScanner) (MigrationSummary, error) {
	var item MigrationSummary
	var version pgtype.Int4
	var revID, revMigID pgtype.Int8
	var revNo, batchSize pgtype.Int4
	var revStatus, sqlUp, sqlDown, irrevReason, mode, batchTable, batchCol, revCreator pgtype.Text
	var irrev pgtype.Bool
	var revCreatedAt, revSubmittedAt pgtype.Timestamptz
	var latestCRStatus pgtype.Text

	if err := row.Scan(
		&item.ID, &item.DatabaseID, &version, &item.IsApplied, &item.Title,
		&item.CreatedBy, &item.CreatedAt,
		&revID, &revMigID, &revNo, &revStatus,
		&sqlUp, &sqlDown, &irrev, &irrevReason,
		&mode, &batchTable, &batchCol, &batchSize,
		&revCreator, &revCreatedAt, &revSubmittedAt,
		&latestCRStatus,
	); err != nil {
		return MigrationSummary{}, err
	}

	if version.Valid {
		v := int(version.Int32)
		item.Version = &v
	}
	if latestCRStatus.Valid {
		item.LatestRequestStatus = &latestCRStatus.String
	}
	if revID.Valid {
		rev := &Revision{
			ID:           revID.Int64,
			MigrationID:  revMigID.Int64,
			RevisionNo:   int(revNo.Int32),
			Status:       revStatus.String,
			SQLUp:        sqlUp.String,
			Irreversible: irrev.Bool,
			Mode:         mode.String,
			CreatedBy:    revCreator.String,
			CreatedAt:    revCreatedAt.Time,
		}
		if sqlDown.Valid {
			rev.SQLDown = &sqlDown.String
		}
		if irrevReason.Valid {
			rev.IrreversibleReason = &irrevReason.String
		}
		if batchTable.Valid {
			rev.BatchTable = &batchTable.String
		}
		if batchCol.Valid {
			rev.BatchColumn = &batchCol.String
		}
		if batchSize.Valid {
			bs := int(batchSize.Int32)
			rev.BatchSize = &bs
		}
		if revSubmittedAt.Valid {
			t := revSubmittedAt.Time
			rev.SubmittedAt = &t
		}
		item.CurrentRevision = rev
	}
	return item, nil
}

func scanRevision(row rowScanner) (Revision, error) {
	var rev Revision
	var sqlDown, irrevReason, batchTable, batchCol pgtype.Text
	var batchSize pgtype.Int4
	var submittedAt pgtype.Timestamptz

	if err := row.Scan(
		&rev.ID, &rev.MigrationID, &rev.RevisionNo, &rev.Status,
		&rev.SQLUp, &sqlDown, &rev.Irreversible, &irrevReason,
		&rev.Mode, &batchTable, &batchCol, &batchSize,
		&rev.CreatedBy, &rev.CreatedAt, &submittedAt,
	); err != nil {
		return Revision{}, err
	}
	if sqlDown.Valid {
		rev.SQLDown = &sqlDown.String
	}
	if irrevReason.Valid {
		rev.IrreversibleReason = &irrevReason.String
	}
	if batchTable.Valid {
		rev.BatchTable = &batchTable.String
	}
	if batchCol.Valid {
		rev.BatchColumn = &batchCol.String
	}
	if batchSize.Valid {
		bs := int(batchSize.Int32)
		rev.BatchSize = &bs
	}
	if submittedAt.Valid {
		t := submittedAt.Time
		rev.SubmittedAt = &t
	}
	return rev, nil
}

func (h *Handler) migrationDatabaseID(ctx context.Context, id int64) (int64, error) {
	var dbID int64
	err := h.platform.QueryRow(ctx, `SELECT db_connection_id FROM migrations WHERE id = $1`, id).Scan(&dbID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errMigrationNotFound
	}
	return dbID, err
}

func (h *Handler) databaseExists(w http.ResponseWriter, ctx context.Context, id int64) bool {
	var exists bool
	if err := h.platform.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM db_connections WHERE id = $1)`, id).Scan(&exists); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal memeriksa database")
		return false
	}
	if !exists {
		web.WriteError(w, http.StatusNotFound, "Database tidak ditemukan")
		return false
	}
	return true
}
