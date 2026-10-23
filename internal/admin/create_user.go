package admin

import (
	"net/http"
	"strings"
)

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	IsAdmin  bool   `json:"isAdmin"`
}

func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdminUser(w, r); !ok {
		return
	}

	var input createUserRequest
	if !decodeUserBody(w, r, &input) {
		return
	}

	input.Username = strings.TrimSpace(input.Username)
	if !validUsername(input.Username) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Username harus berisi 1 sampai 100 karakter tanpa karakter NUL",
		})
		return
	}

	hash, ok := hashUserPassword(w, input.Password)
	if !ok {
		return
	}

	user, err := queryUser(r.Context(), h.db, `
		INSERT INTO users (
			username, password_hash, is_admin, is_active
		)
		VALUES ($1, $2, $3, TRUE)
		RETURNING id, username, is_admin, is_active
	`, input.Username, hash, input.IsAdmin)
	if err != nil {
		writeUserError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, user)
}