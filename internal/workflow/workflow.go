package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrNotImplemented = errors.New("workflow: belum diimplementasikan")
	ErrNotFound       = errors.New("change request tidak ditemukan")
)

type RequestInput struct {
	DatabaseID        int64
	MigrationID       int64
	RevisionID        int64
	Action            string
	TargetExecutionID *string
	RequestedBy       int64
}

type GroupApproval struct {
	GroupID        int64      `json:"groupId"`
	GroupName      string     `json:"groupName"`
	LeaderID       *int64     `json:"leaderId"`
	LeaderUsername *string    `json:"leaderUsername"`
	Decision       *string    `json:"decision"`
	Comment        *string    `json:"comment"`
	DecidedAt      *time.Time `json:"decidedAt"`
}

type Status struct {
	Approved       bool            `json:"approved"`
	Rejected       bool            `json:"rejected"`
	MissingLeaders []string        `json:"missingLeaders"`
	Groups         []GroupApproval `json:"groups"`
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
	var dbID int64
	err := q.QueryRow(ctx, `
		SELECT db_connection_id
		FROM change_requests
		WHERE id = $1
	`, changeRequestID).Scan(&dbID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Status{}, ErrNotFound
	}
	if err != nil {
		return Status{}, fmt.Errorf("baca change request %d: %w", changeRequestID, err)
	}
	rows, err := q.Query(ctx, `
		SELECT
			g.id,
			g.name,
			CASE WHEN u.is_active THEN g.leader_user_id ELSE NULL END AS leader_id,
			CASE WHEN u.is_active THEN u.username ELSE NULL END AS leader_username,
			a.decision,
			a.comment,
			a.created_at
		FROM group_db_access gda
		JOIN groups g ON g.id = gda.group_id
		LEFT JOIN users u ON u.id = g.leader_user_id
		LEFT JOIN approvals a
			ON a.change_request_id = $1
			AND a.group_id = g.id
			AND g.leader_user_id IS NOT NULL
			AND u.is_active = TRUE
			AND a.approver_id = g.leader_user_id
		WHERE gda.db_connection_id = $2
		ORDER BY g.id ASC
	`, changeRequestID, dbID)
	if err != nil {
		return Status{}, fmt.Errorf("baca daftar approval group: %w", err)
	}
	defer rows.Close()
	st := Status{
		MissingLeaders: make([]string, 0),
		Groups:         make([]GroupApproval, 0),
	}
	allApproved := true
	for rows.Next() {
		var ga GroupApproval
		var leaderID pgtype.Int8
		var leaderName, decision, comment pgtype.Text
		var decidedAt pgtype.Timestamptz
		if err := rows.Scan(
			&ga.GroupID, &ga.GroupName, &leaderID, &leaderName,
			&decision, &comment, &decidedAt,
		); err != nil {
			return Status{}, fmt.Errorf("scan approval group: %w", err)
		}
		if leaderID.Valid {
			v := leaderID.Int64
			ga.LeaderID = &v
		} else {
			st.MissingLeaders = append(st.MissingLeaders, ga.GroupName)
			allApproved = false
		}
		if leaderName.Valid {
			v := leaderName.String
			ga.LeaderUsername = &v
		}
		if decision.Valid {
			v := decision.String
			ga.Decision = &v
			if v == "reject" {
				st.Rejected = true
			}
			if v != "approve" {
				allApproved = false
			}
		} else {
			allApproved = false
		}
		if comment.Valid {
			v := comment.String
			ga.Comment = &v
		}
		if decidedAt.Valid {
			t := decidedAt.Time
			ga.DecidedAt = &t
		}
		st.Groups = append(st.Groups, ga)
	}
	if err := rows.Err(); err != nil {
		return Status{}, fmt.Errorf("iterasi approval group: %w", err)
	}
	if len(st.Groups) == 0 {
		allApproved = false
	}
	st.Approved = !st.Rejected && allApproved
	return st, nil
}

func Revalidate(ctx context.Context, tx pgx.Tx, changeRequestID int64) error {
	var (
		dbID        int64
		migrationID int64
		revisionID  int64
		action      string
		targetExec  pgtype.Text
		status      string
		requestedBy int64
	)
	err := tx.QueryRow(ctx, `
		SELECT db_connection_id, migration_id, revision_id, action,
		       target_execution_id::text, status, requested_by
		FROM change_requests
		WHERE id = $1
		FOR UPDATE
	`, changeRequestID).Scan(
		&dbID, &migrationID, &revisionID, &action,
		&targetExec, &status, &requestedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("baca change request %d: %w", changeRequestID, err)
	}
	if status != "queued" && status != "running" && status != "approved" {
		return fmt.Errorf("status change request tidak valid untuk dijalankan (%s)", status)
	}
	st, err := ApprovalStatus(ctx, tx, changeRequestID)
	if err != nil {
		return err
	}
	if st.Rejected {
		return errors.New("change request telah ditolak oleh leader")
	}
	if len(st.Groups) == 0 {
		return errors.New("tidak ada group yang memiliki akses ke database ini")
	}
	if len(st.MissingLeaders) > 0 {
		return fmt.Errorf("group berikut belum memiliki leader aktif: %s", strings.Join(st.MissingLeaders, ", "))
	}
	if !st.Approved {
		pending := make([]string, 0)
		for _, g := range st.Groups {
			if g.Decision == nil || *g.Decision != "approve" {
				pending = append(pending, g.GroupName)
			}
		}
		return fmt.Errorf("approval group berikut belum lengkap: %s", strings.Join(pending, ", "))
	}
	var requesterActive bool
	err = tx.QueryRow(ctx, `SELECT is_active FROM users WHERE id = $1`, requestedBy).Scan(&requesterActive)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !requesterActive) {
		return errors.New("pengaju change request tidak lagi aktif")
	}
	if err != nil {
		return fmt.Errorf("cek status pengaju: %w", err)
	}
	var hasEditAccess bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM group_members gm
			JOIN group_db_access gda ON gda.group_id = gm.group_id
			WHERE gm.user_id = $1
			  AND gda.db_connection_id = $2
			  AND gda.level = 'edit'
		)
	`, requestedBy, dbID).Scan(&hasEditAccess)
	if err != nil {
		return fmt.Errorf("cek akses pengaju: %w", err)
	}
	if !hasEditAccess {
		return errors.New("pengaju tidak lagi memiliki akses Edit ke database ini")
	}
	// Cek tidak ada job partial/uncertain lain yang menahan database.
	var hasBlockingJob bool
	if (action == "resume" || action == "revert") && targetExec.Valid {
		err = tx.QueryRow(ctx, `
			WITH RECURSIVE up AS (
				SELECT j.execution_id, cr.action, cr.target_execution_id, 0 AS depth
				FROM jobs j
				JOIN change_requests cr ON cr.id = j.change_request_id
				WHERE j.execution_id = $3::uuid
				UNION ALL
				SELECT j.execution_id, cr.action, cr.target_execution_id, up.depth + 1
				FROM up
				JOIN jobs j ON j.execution_id = up.target_execution_id
				JOIN change_requests cr ON cr.id = j.change_request_id
				WHERE up.action IN ('resume', 'revert') AND up.depth < 100
			),
			root AS (
				SELECT execution_id FROM up ORDER BY depth DESC LIMIT 1
			),
			fam AS (
				SELECT execution_id, 0 AS depth FROM root
				UNION ALL
				SELECT j.execution_id, fam.depth + 1
				FROM fam
				JOIN change_requests cr ON cr.target_execution_id = fam.execution_id
					AND cr.action IN ('resume', 'revert')
				JOIN jobs j ON j.change_request_id = cr.id
				WHERE fam.depth < 100
			)
			SELECT EXISTS (
				SELECT 1
				FROM jobs
				WHERE db_connection_id = $1
				  AND status IN ('partial', 'uncertain')
				  AND change_request_id <> $2
				  AND execution_id NOT IN (SELECT execution_id FROM fam)
			)
		`, dbID, changeRequestID, targetExec.String).Scan(&hasBlockingJob)
	} else {
		err = tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM jobs
				WHERE db_connection_id = $1
				  AND status IN ('partial', 'uncertain')
				  AND change_request_id <> $2
			)
		`, dbID, changeRequestID).Scan(&hasBlockingJob)
	}
	if err != nil {
		return fmt.Errorf("cek job partial/uncertain: %w", err)
	}
	if hasBlockingJob {
		return errors.New("database masih memiliki job berstatus partial atau uncertain yang harus diselesaikan dulu")
	}
	return revalidatePosition(ctx, tx, dbID, migrationID, revisionID, action, targetExec)
}
func revalidatePosition(
	ctx context.Context,
	tx pgx.Tx,
	dbID, migrationID, revisionID int64,
	action string,
	targetExec pgtype.Text,
) error {
	var version pgtype.Int4
	var isApplied bool
	err := tx.QueryRow(ctx, `
		SELECT version, is_applied
		FROM migrations
		WHERE id = $1 AND db_connection_id = $2
	`, migrationID, dbID).Scan(&version, &isApplied)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("migration tidak ditemukan pada database ini")
	}
	if err != nil {
		return fmt.Errorf("baca posisi migration: %w", err)
	}
	var revStatus string
	var irreversible bool
	err = tx.QueryRow(ctx, `
		SELECT status, irreversible
		FROM migration_revisions
		WHERE id = $1 AND migration_id = $2
	`, revisionID, migrationID).Scan(&revStatus, &irreversible)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("revisi migration tidak ditemukan")
	}
	if err != nil {
		return fmt.Errorf("baca revisi migration: %w", err)
	}
	if revStatus != "submitted" {
		return errors.New("revisi migration belum berstatus submitted")
	}
	switch action {
	case "apply":
		if version.Valid || isApplied {
			return errors.New("migration sudah pernah diterapkan sebelumnya")
		}
		return nil
	case "rollback":
		if !isApplied || !version.Valid {
			return errors.New("rollback hanya dapat dilakukan pada migration yang sedang aktif")
		}
		if irreversible {
			return errors.New("migration bersifat irreversible sehingga tidak dapat di-rollback")
		}
		var maxActive pgtype.Int4
		if err := tx.QueryRow(ctx, `
			SELECT MAX(version)
			FROM migrations
			WHERE db_connection_id = $1 AND is_applied = TRUE
		`, dbID).Scan(&maxActive); err != nil {
			return fmt.Errorf("cek versi aktif teratas: %w", err)
		}
		if !maxActive.Valid || version.Int32 != maxActive.Int32 {
			return errors.New("rollback hanya dapat dilakukan pada migration aktif dengan versi teratas")
		}
		return nil
	case "forward":
		if isApplied || !version.Valid {
			return errors.New("forward hanya dapat dilakukan pada migration yang sedang di-rollback")
		}
		var activeVersion int32
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(version), 0)
			FROM migrations
			WHERE db_connection_id = $1 AND is_applied = TRUE
		`, dbID).Scan(&activeVersion); err != nil {
			return fmt.Errorf("cek posisi aktif: %w", err)
		}
		var nextVersion pgtype.Int4
		if err := tx.QueryRow(ctx, `
			SELECT MIN(version)
			FROM migrations
			WHERE db_connection_id = $1 AND version > $2 AND is_applied = FALSE
		`, dbID, activeVersion).Scan(&nextVersion); err != nil {
			return fmt.Errorf("cek urutan forward berikutnya: %w", err)
		}
		if !nextVersion.Valid || version.Int32 != nextVersion.Int32 {
			return errors.New("forward hanya dapat dilakukan pada versi terkecil tepat di atas posisi aktif")
		}
		return nil
	case "resume", "revert":
		if !targetExec.Valid || strings.TrimSpace(targetExec.String) == "" {
			return fmt.Errorf("aksi %s wajib merujuk target execution ID", action)
		}
		var jobStatus string
		err := tx.QueryRow(ctx, `
			SELECT j.status
			FROM jobs j
			JOIN change_requests cr ON cr.id = j.change_request_id
			WHERE j.execution_id::text = $1
			  AND j.db_connection_id = $2
			  AND cr.migration_id = $3
		`, targetExec.String, dbID, migrationID).Scan(&jobStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("job target untuk %s tidak ditemukan", action)
		}
		if err != nil {
			return fmt.Errorf("cek job target %s: %w", action, err)
		}
		if jobStatus != "partial" {
			return fmt.Errorf("aksi %s hanya dapat dilakukan pada job berstatus partial (saat ini: %s)", action, jobStatus)
		}
		return nil
	default:
		return fmt.Errorf("aksi change request tidak dikenal: %s", action)
	}
}
