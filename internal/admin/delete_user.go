package admin

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireAdminUser(w, r)
	if !ok {
		return
	}

	id, ok := parseUserID(w, r)
	if !ok {
		return
	}

	if id == actor.ID {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Tidak dapat menghapus akun sendiri",
		})
		return
	}

	h.runUserTransaction(w, r, http.StatusNoContent,
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			if _, err := queryUser(ctx, tx, `
				SELECT id, username, is_admin, is_active
				FROM users
				WHERE id = $1
				FOR UPDATE
			`, id); err != nil {
				return nil, err
			}

			sessions, err := h.sessionManager(tx)
			if err != nil {
				return nil, err
			}

			if err := sessions.RevokeUserSessions(ctx, id); err != nil {
				return nil, err
			}

			if _, err := queryUser(ctx, tx, `
				DELETE FROM users
				WHERE id = $1
				RETURNING id, username, is_admin, is_active
			`, id); err != nil {
				return nil, err
			}

			return nil, nil
		},
	)
}
