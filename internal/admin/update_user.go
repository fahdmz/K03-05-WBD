package admin

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

type updateUserRequest struct {
	Username *string `json:"username"`
	Password *string `json:"password"`
	IsAdmin  *bool   `json:"isAdmin"`
	IsActive *bool   `json:"isActive"`
}

func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireAdminUser(w, r)
	if !ok {
		return
	}

	id, ok := parseUserID(w, r)
	if !ok {
		return
	}

	var input updateUserRequest
	if !decodeUserBody(w, r, &input) {
		return
	}

	if input.Username == nil && input.Password == nil &&
		input.IsAdmin == nil && input.IsActive == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Kirim minimal satu field untuk diubah",
		})
		return
	}

	if id == actor.ID {
		disablesSelf := input.IsActive != nil && !*input.IsActive
		demotesSelf := input.IsAdmin != nil && !*input.IsAdmin

		if disablesSelf || demotesSelf {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "Tidak dapat menonaktifkan atau menurunkan hak admin sendiri",
			})
			return
		}
	}

	if input.Username != nil {
		name := strings.TrimSpace(*input.Username)
		if !validUsername(name) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Username harus berisi 1 sampai 100 karakter tanpa karakter NUL",
			})
			return
		}
		input.Username = &name
	}

	var passwordHash *string
	if input.Password != nil {
		hash, ok := hashUserPassword(w, *input.Password)
		if !ok {
			return
		}
		passwordHash = &hash
	}

	h.runUserTransaction(w, r, http.StatusOK,
		func(ctx context.Context, tx pgx.Tx) (any, error) {
			before, err := queryUser(ctx, tx, `
				SELECT id, username, is_admin, is_active
				FROM users
				WHERE id = $1
				FOR UPDATE
			`, id)
			if err != nil {
				return nil, err
			}

			nextAdmin := before.IsAdmin
			if input.IsAdmin != nil {
				nextAdmin = *input.IsAdmin
			}

			nextActive := before.IsActive
			if input.IsActive != nil {
				nextActive = *input.IsActive
			}

			changes := newSessionChanges()

			revoke := input.Password != nil ||
				(before.IsAdmin && !nextAdmin) ||
				(before.IsActive && !nextActive)

			if revoke {
				changes.Revoke(id)
			} else if !before.IsAdmin && nextAdmin {
				changes.Rotate(id)
			}

			var sessions SessionManager
			if len(changes.revoke) > 0 || len(changes.rotate) > 0 {
				sessions, err = h.sessionManager(tx)
				if err != nil {
					return nil, err
				}
			}

			user, err := queryUser(ctx, tx, `
				UPDATE users
				SET
					username = COALESCE($2::text, username),
					password_hash = COALESCE($3::text, password_hash),
					is_admin = COALESCE($4::boolean, is_admin),
					is_active = COALESCE($5::boolean, is_active)
				WHERE id = $1
				RETURNING id, username, is_admin, is_active
			`, id, input.Username, passwordHash,
				input.IsAdmin, input.IsActive)
			if err != nil {
				return nil, err
			}

			if sessions != nil {
				if err := changes.Apply(ctx, sessions); err != nil {
					return nil, err
				}
			}

			return user, nil
		},
	)
}
