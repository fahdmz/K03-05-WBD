package admin

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/authz"
	"github.com/jackc/pgx/v5"
)

type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type Handler struct {
	db Querier
}

func NewHandler(db Querier) *Handler {
	return &Handler{db: db}
}

type userResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"isAdmin"`
	IsActive bool   `json:"isActive"`
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok || u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "Silakan login terlebih dahulu",
		})
		return
	}

	if err := authz.RequireAdmin(u); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "Hanya admin yang dapat mengakses daftar user",
		})
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT id, username, is_admin, is_active
		FROM users
		ORDER BY id
	`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Gagal mengambil daftar user",
		})
		return
	}
	defer rows.Close()

	users := make([]userResponse, 0)

	for rows.Next() {
		var user userResponse
		if err := rows.Scan(
			&user.ID,
			&user.Username,
			&user.IsAdmin,
			&user.IsActive,
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Gagal membaca data user",
			})
			return
		}

		users = append(users, user)
	}

	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Gagal membaca daftar user",
		})
		return
	}

	writeJSON(w, http.StatusOK, users)
}