package admin

import "net/http"

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

	_, err := queryUser(r.Context(), h.db, `
		DELETE FROM users
		WHERE id = $1
		RETURNING id, username, is_admin, is_active
	`, id)
	if err != nil {
		writeUserError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}