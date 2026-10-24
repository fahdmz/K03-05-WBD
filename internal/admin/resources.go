package admin

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ResourceHandler struct {
	db      *pgxpool.Pool
	manager dbmanager.Manager
	aead    cipher.AEAD
	sessionFactory SessionManagerFactory
}

func NewResourceHandler(
	db *pgxpool.Pool,
	manager dbmanager.Manager,
	encodedKey string,
) (*ResourceHandler, error) {
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("DB_ENCRYPTION_KEY harus base64 dari 32 byte")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	if db == nil || manager == nil {
		return nil, errors.New("database dan manager wajib tersedia")
	}

	return &ResourceHandler{
		db: db, manager: manager, aead: aead,
	}, nil
}

func (h *ResourceHandler) encryptPassword(password string) ([]byte, error) {
	nonce := make([]byte, h.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	return h.aead.Seal(nonce, nonce, []byte(password), nil), nil
}

type resourceError struct {
	status  int
	message string
}

func (e *resourceError) Error() string {
	return e.message
}

func badInput(message string) error {
	return &resourceError{http.StatusBadRequest, message}
}

func resourceFailure(w http.ResponseWriter, err error) {
	var inputErr *resourceError
	if errors.As(err, &inputErr) {
		writeJSON(w, inputErr.status, map[string]string{
			"error": inputErr.message,
		})
		return
	}

	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "Data tidak ditemukan",
		})
		return
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "Nama atau relasi tersebut sudah digunakan",
			})
			return
		case "23503":
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "Data terkait tidak ditemukan atau masih digunakan",
			})
			return
		}
	}

	writeJSON(w, http.StatusInternalServerError, map[string]string{
		"error": "Gagal memproses permintaan",
	})
}

func resourceID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, badInput("ID harus berupa bilangan positif")
	}
	return id, nil
}

func validResourceName(value string) bool {
	return strings.TrimSpace(value) != "" &&
		utf8.RuneCountInString(value) <= 100 &&
		!strings.ContainsRune(value, '\x00')
}

func rollbackResourceTx(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

type resourceAction func(context.Context, pgx.Tx) (any, error)

func (h *ResourceHandler) run(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	action resourceAction,
	afterCommit func(),
) {
	if _, ok := requireAdminUser(w, r); !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tx, err := h.db.Begin(ctx)
	if err != nil {
		resourceFailure(w, err)
		return
	}
	defer rollbackResourceTx(tx)

	result, err := action(ctx, tx)
	if err != nil {
		resourceFailure(w, err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		resourceFailure(w, err)
		return
	}

	if afterCommit != nil {
		afterCommit()
	}

	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}

	writeJSON(w, status, result)
}

func lockGroup(ctx context.Context, tx pgx.Tx, id int64) error {
	var found int64
	return tx.QueryRow(ctx,
		"SELECT id FROM groups WHERE id = $1 FOR UPDATE", id,
	).Scan(&found)
}

func revokeGroupSessions(ctx context.Context, tx pgx.Tx, id int64) error {
	_, err := tx.Exec(ctx, `
		UPDATE sessions
		SET revoked_at = NOW()
		WHERE revoked_at IS NULL
		  AND user_id IN (
			SELECT user_id FROM group_members WHERE group_id = $1
		  )
	`, id)
	return err
}

func (h *ResourceHandler) RegisterRoutes(mux *http.ServeMux) {
	routes := map[string]http.HandlerFunc{
		"GET /api/admin/groups":                 h.ListGroups,
		"POST /api/admin/groups":                h.CreateGroup,
		"PATCH /api/admin/groups/{id}":          h.UpdateGroup,
		"DELETE /api/admin/groups/{id}":         h.DeleteGroup,
		"PUT /api/admin/groups/{id}/members":    h.SetGroupMembers,
		"PUT /api/admin/groups/{id}/access":     h.SetGroupAccess,
		"GET /api/admin/databases":              h.ListDatabases,
		"POST /api/admin/databases":             h.CreateDatabase,
		"PATCH /api/admin/databases/{id}":        h.UpdateDatabase,
		"DELETE /api/admin/databases/{id}":       h.DeleteDatabase,
		"POST /api/admin/databases/test":         h.TestDatabase,
	}

	for pattern, handler := range routes {
		mux.Handle(pattern, auth.RequireLogin(handler))
	}
}

func (h *ResourceHandler) WithSessionManagerFactory(
	factory SessionManagerFactory,
) *ResourceHandler {
	copy := *h
	copy.sessionFactory = factory
	return &copy
}

func (h *ResourceHandler) sessionManager(tx pgx.Tx) (SessionManager, error) {
	if h.sessionFactory == nil {
		return nil, &resourceError{
			status:  http.StatusServiceUnavailable,
			message: "Pengelolaan session belum tersedia",
		}
	}

	manager := h.sessionFactory(tx)
	if manager == nil {
		return nil, errors.New("session manager factory returned nil")
	}

	return manager, nil
}

