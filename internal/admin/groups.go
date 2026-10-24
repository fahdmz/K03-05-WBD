package admin

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type groupResponse struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type groupDetailResponse struct {
	groupResponse
	MemberIDs []int64            `json:"memberIds"`
	Access    []groupAccessEntry `json:"access"`
}

func scanGroup(row pgx.Row) (groupResponse, error) {
	var group groupResponse
	err := row.Scan(&group.ID, &group.Name, &group.Description)
	return group, err
}

func (h *ResourceHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	h.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := tx.Query(ctx, `
			SELECT
				g.id,
				g.name,
				g.description,
				ARRAY(
					SELECT gm.user_id
					FROM group_members gm
					WHERE gm.group_id = g.id
					ORDER BY gm.user_id
				) AS member_ids,
				COALESCE(
					(
						SELECT jsonb_agg(
							jsonb_build_object(
								'databaseId', ga.db_connection_id,
								'level', ga.level
							)
							ORDER BY ga.db_connection_id
						)
						FROM group_db_access ga
						WHERE ga.group_id = g.id
					),
					'[]'::jsonb
				) AS access
			FROM groups g
			ORDER BY g.id
		`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		groups := make([]groupDetailResponse, 0)
		for rows.Next() {
			var group groupDetailResponse
			if err := rows.Scan(
				&group.ID,
				&group.Name,
				&group.Description,
				&group.MemberIDs,
				&group.Access,
			); err != nil {
				return nil, err
			}
			groups = append(groups, group)
		}
		return groups, rows.Err()
	}, nil)
}

func (h *ResourceHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdminUser(w, r); !ok {
		return
	}

	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decodeUserBody(w, r, &input) {
		return
	}

	input.Name = strings.TrimSpace(input.Name)
	if !validResourceName(input.Name) ||
		utf8.RuneCountInString(input.Description) > 2000 ||
		strings.ContainsRune(input.Description, '\x00') {
		resourceFailure(w, badInput("Nama atau deskripsi group tidak valid"))
		return
	}

	h.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return scanGroup(tx.QueryRow(ctx, `
			INSERT INTO groups (name, description)
			VALUES ($1, $2)
			RETURNING id, name, description
		`, input.Name, input.Description))
	}, nil)
}

func (h *ResourceHandler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdminUser(w, r); !ok {
		return
	}

	id, err := resourceID(r)
	if err != nil {
		resourceFailure(w, err)
		return
	}

	var input struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if !decodeUserBody(w, r, &input) {
		return
	}

	if input.Name == nil && input.Description == nil {
		resourceFailure(w, badInput("Kirim minimal satu field"))
		return
	}

	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if !validResourceName(name) {
			resourceFailure(w, badInput("Nama group tidak valid"))
			return
		}
		input.Name = &name
	}

	if input.Description != nil &&
		(utf8.RuneCountInString(*input.Description) > 2000 ||
			strings.ContainsRune(*input.Description, '\x00')) {
		resourceFailure(w, badInput("Deskripsi group tidak valid"))
		return
	}

	h.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return scanGroup(tx.QueryRow(ctx, `
			UPDATE groups
			SET name = COALESCE($2::text, name),
			    description = COALESCE($3::text, description)
			WHERE id = $1
			RETURNING id, name, description
		`, id, input.Name, input.Description))
	}, nil)
}

func (h *ResourceHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	h.run(w, r, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx) (any, error) {
		id, err := resourceID(r)
		if err != nil {
			return nil, err
		}
		if err := lockGroup(ctx, tx, id); err != nil {
			return nil, err
		}
		var memberIDs []int64
		if err := tx.QueryRow(ctx, `
			SELECT ARRAY(SELECT user_id FROM group_members WHERE group_id = $1)
		`, id).Scan(&memberIDs); err != nil {
			return nil, err
		}
		if err := h.revokeUserSessions(ctx, tx, memberIDs); err != nil {
			return nil, err
		}

		_, err = tx.Exec(ctx, "DELETE FROM groups WHERE id = $1", id)
		return nil, err
	}, nil)
}

func (h *ResourceHandler) SetGroupMembers(
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
		UserIDs *[]int64 `json:"userIds"`
	}
	if !decodeUserBody(w, r, &input) {
		return
	}
	if input.UserIDs == nil || len(*input.UserIDs) > 1000 {
		resourceFailure(w, badInput("userIds wajib berupa array maksimal 1000 ID"))
		return
	}

	seen := make(map[int64]bool)
	for _, userID := range *input.UserIDs {
		if userID <= 0 || seen[userID] {
			resourceFailure(w, badInput("ID user tidak valid atau duplikat"))
			return
		}
		seen[userID] = true
	}

	h.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := lockGroup(ctx, tx, id); err != nil {
			return nil, err
		}

		var count int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM users
			WHERE id = ANY($1::bigint[])
		`, *input.UserIDs).Scan(&count); err != nil {
			return nil, err
		}
		if count != len(*input.UserIDs) {
			return nil, badInput("Ada user yang tidak ditemukan")
		}

		var previousMembers []int64
		if err := tx.QueryRow(ctx, `
			SELECT ARRAY(
				SELECT user_id
				FROM group_members
				WHERE group_id = $1
				ORDER BY user_id
			)
		`, id).Scan(&previousMembers); err != nil {
			return nil, err
		}

		changes := membershipSessionChanges(previousMembers, *input.UserIDs)

		if len(changes.revoke) == 0 && len(changes.rotate) == 0 {
			return map[string]any{
				"groupId": id,
				"userIds": *input.UserIDs,
			}, nil
		}

		sessions, err := h.sessionManager(tx)
		if err != nil {
			return nil, err
		}

		if _, err := tx.Exec(ctx,
			"DELETE FROM group_members WHERE group_id = $1", id,
		); err != nil {
			return nil, err
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO group_members (group_id, user_id)
			SELECT $1, user_id
			FROM unnest($2::bigint[]) AS member(user_id)
		`, id, *input.UserIDs); err != nil {
			return nil, err
		}

		if err := changes.Apply(ctx, sessions); err != nil {
			return nil, err
		}

		return map[string]any{
			"groupId": id,
			"userIds": *input.UserIDs,
		}, nil
	}, nil)
}

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
			SELECT COUNT(*) FROM db_connections
			WHERE id = ANY($1::bigint[])
		`, ids).Scan(&count); err != nil {
			return nil, err
		}
		if count != len(ids) {
			return nil, badInput("Ada database yang tidak ditemukan")
		}

		rows, err := tx.Query(ctx, `
			SELECT db_connection_id, level
			FROM group_db_access
			WHERE group_id = $1
		`, id)
		if err != nil {
			return nil, err
		}

		previous, err := pgx.CollectRows(rows,
			func(row pgx.CollectableRow) (groupAccessEntry, error) {
				var entry groupAccessEntry
				err := row.Scan(&entry.DatabaseID, &entry.Level)
				return entry, err
			},
		)
		if err != nil {
			return nil, err
		}

		result := map[string]any{
			"groupId": id,
			"access":  *input.Access,
		}

		oldLevels := make(map[int64]string, len(previous))
		for _, entry := range previous {
			oldLevels[entry.DatabaseID] = entry.Level
		}

		unchanged := len(previous) == len(*input.Access)
		for _, entry := range *input.Access {
			if oldLevels[entry.DatabaseID] != entry.Level {
				unchanged = false
				break
			}
		}
		if unchanged {
			return result, nil
		}

		var memberIDs []int64
		if err := tx.QueryRow(ctx, `
			SELECT ARRAY(
				SELECT user_id
				FROM group_members
				WHERE group_id = $1
				ORDER BY user_id
			)
		`, id).Scan(&memberIDs); err != nil {
			return nil, err
		}

		changes := accessSessionChanges(memberIDs, previous, *input.Access)

		var sessions SessionManager
		if len(changes.revoke) > 0 || len(changes.rotate) > 0 {
			sessions, err = h.sessionManager(tx)
			if err != nil {
				return nil, err
			}
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

		if sessions != nil {
			if err := changes.Apply(ctx, sessions); err != nil {
				return nil, err
			}
		}

		return result, nil
	}, nil)
}
