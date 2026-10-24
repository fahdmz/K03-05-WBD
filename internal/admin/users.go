package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/authz"
	"github.com/jackc/pgx/v5"
)

type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type Handler struct {
	db             Querier
	sessionFactory SessionManagerFactory
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
