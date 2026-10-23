package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/authz"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

func requireAdminUser(
	w http.ResponseWriter,
	r *http.Request,
) (*auth.User, bool) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok || u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "Silakan login terlebih dahulu",
		})
		return nil, false
	}

	if err := authz.RequireAdmin(u); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "Hanya admin yang dapat mengelola user",
		})
		return nil, false
	}

	return u, true
}

func decodeUserBody(
	w http.ResponseWriter,
	r *http.Request,
	dest any,
) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dest); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Body JSON tidak valid",
		})
		return false
	}

	if err := decoder.Decode(new(any)); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Body harus berisi tepat satu objek JSON",
		})
		return false
	}

	return true
}

func parseUserID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "ID user tidak valid",
		})
		return 0, false
	}
	return id, true
}

func validUsername(username string) bool {
	return strings.TrimSpace(username) != "" &&
		utf8.RuneCountInString(username) <= 100 &&
		!strings.ContainsRune(username, '\x00')
}

func hashUserPassword(
	w http.ResponseWriter,
	password string,
) (string, bool) {
	if utf8.RuneCountInString(password) < 8 || len(password) > 72 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Password minimal 8 karakter dan maksimal 72 byte",
		})
		return "", false
	}

	hash, err := bcrypt.GenerateFromPassword(
		[]byte(password), bcrypt.DefaultCost,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Gagal memproses password",
		})
		return "", false
	}

	return string(hash), true
}

func queryUser(
	ctx context.Context,
	db Querier,
	query string,
	args ...any,
) (userResponse, error) {
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return userResponse{}, err
	}

	return pgx.CollectExactlyOneRow(
		rows,
		func(row pgx.CollectableRow) (userResponse, error) {
			var user userResponse
			err := row.Scan(
				&user.ID,
				&user.Username,
				&user.IsAdmin,
				&user.IsActive,
			)
			return user, err
		},
	)
}

func writeUserError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "User tidak ditemukan",
		})
		return
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "23505" &&
			pgErr.ConstraintName == "users_username_key" {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "Username sudah digunakan",
			})
			return
		}

		if pgErr.Code == "23503" {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "User masih terkait data lain; nonaktifkan user tersebut",
			})
			return
		}
	}

	writeJSON(w, http.StatusInternalServerError, map[string]string{
		"error": "Gagal memproses data user",
	})
}