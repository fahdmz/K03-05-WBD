package admin

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

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
			SELECT COUNT(*) FROM users WHERE id = ANY($1::bigint[])
		`, *input.UserIDs).Scan(&count); err != nil {
			return nil, err
		}
		if count != len(*input.UserIDs) {
			return nil, badInput("Ada user yang tidak ditemukan")
		}

		if err := revokeGroupSessions(ctx, tx, id); err != nil {
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

		if err := revokeGroupSessions(ctx, tx, id); err != nil {
			return nil, err
		}

		return map[string]any{
			"groupId": id,
			"userIds": *input.UserIDs,
		}, nil
	}, nil)
}