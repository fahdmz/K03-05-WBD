package admin

import (
	"net/http"
	"strings"
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

	revokeSessions := input.Password != nil ||
		input.IsAdmin != nil ||
		input.IsActive != nil

	user, err := queryUser(r.Context(), h.db, `
		WITH updated AS (
			UPDATE users
			SET
				username = COALESCE($2::text, username),
				password_hash = COALESCE($3::text, password_hash),
				is_admin = COALESCE($4::boolean, is_admin),
				is_active = COALESCE($5::boolean, is_active)
			WHERE id = $1
			RETURNING id, username, is_admin, is_active
		),
		revoked AS (
			UPDATE sessions
			SET revoked_at = NOW()
			WHERE user_id IN (SELECT id FROM updated)
				AND revoked_at IS NULL
				AND $6::boolean
			RETURNING id
		)
		SELECT id, username, is_admin, is_active
		FROM updated
	`, id, input.Username, passwordHash,
		input.IsAdmin, input.IsActive, revokeSessions)
	if err != nil {
		writeUserError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, user)
}