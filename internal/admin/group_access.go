package admin

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

type groupAccessEntry struct {
	DatabaseID int64  `json:"databaseId"`
	Level      string `json:"level"`
}

func (h *ResourceHandler) SetGroupAccess(
	w http.ResponseWriter,
	r *http.Request,
) {
	if _, ok := requireAdminUser(w, r); !ok {
		return
	}

	id, err := resourceID(r)
	if err != nil {
		resourceFailure(w, err)
		return
	}

	var input struct {
		Access *[]groupAccessEntry `json:"access"`
	}
	if !decodeUserBody(w, r, &input) {
		return
	}
	if input.Access == nil || len(*input.Access) > 1000 {
		resourceFailure(w, badInput("access wajib berupa array maksimal 1000 entri"))
		return
	}

	ids := make([]int64, 0, len(*input.Access))
	levels := make([]string, 0, len(*input.Access))
	seen := make(map[int64]bool)

	for _, entry := range *input.Access {
		if entry.DatabaseID <= 0 || seen[entry.DatabaseID] ||
			(entry.Level != "view" && entry.Level != "edit") {
			resourceFailure(w, badInput("Akses database tidak valid atau duplikat"))
			return
		}

		seen[entry.DatabaseID] = true
		ids = append(ids, entry.DatabaseID)
		levels = append(levels, entry.Level)
	}

	h.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := lockGroup(ctx, tx, id); err != nil {
			return nil, err
		}

		var count int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM db_connections WHERE id = ANY($1::bigint[])
		`, ids).Scan(&count); err != nil {
			return nil, err
		}
		if count != len(ids) {
			return nil, badInput("Ada database yang tidak ditemukan")
		}

		if err := revokeGroupSessions(ctx, tx, id); err != nil {
			return nil, err
		}

		if _, err := tx.Exec(ctx,
			"DELETE FROM group_db_access WHERE group_id = $1", id,
		); err != nil {
			return nil, err
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO group_db_access (group_id, db_connection_id, level)
			SELECT $1, database_id, access_level
			FROM unnest($2::bigint[], $3::text[])
				AS entry(database_id, access_level)
		`, id, ids, levels); err != nil {
			return nil, err
		}

		return map[string]any{
			"groupId": id,
			"access":  *input.Access,
		}, nil
	}, nil)
}