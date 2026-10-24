package admin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"

	"github.com/jackc/pgx/v5"
)

type databaseResponse struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	DBName   string `json:"dbname"`
	Username string `json:"username"`
	SSLMode  string `json:"sslmode"`
}

type databaseInput struct {
	Name     *string `json:"name"`
	Host     *string `json:"host"`
	Port     *int    `json:"port"`
	DBName   *string `json:"dbname"`
	Username *string `json:"username"`
	Password *string `json:"password"`
	SSLMode  *string `json:"sslmode"`
}

const databaseColumns = `id, name, host, port, dbname, username, sslmode`

func scanDatabase(row pgx.Row) (databaseResponse, error) {
	var item databaseResponse
	err := row.Scan(
		&item.ID, &item.Name, &item.Host, &item.Port,
		&item.DBName, &item.Username, &item.SSLMode,
	)
	return item, err
}

func validConnection(item databaseResponse) bool {
	if !validResourceName(item.Name) ||
		strings.TrimSpace(item.Host) == "" ||
		strings.TrimSpace(item.DBName) == "" ||
		strings.TrimSpace(item.Username) == "" ||
		item.Port < 1 || item.Port > 65535 {
		return false
	}

	for _, value := range []string{item.Host, item.DBName, item.Username} {
		if len(value) > 255 || strings.ContainsRune(value, '\x00') {
			return false
		}
	}

	switch item.SSLMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		return true
	default:
		return false
	}
}

func applyDatabaseInput(item *databaseResponse, input databaseInput) {
	if input.Name != nil {
		item.Name = strings.TrimSpace(*input.Name)
	}
	if input.Host != nil {
		item.Host = strings.TrimSpace(*input.Host)
	}
	if input.Port != nil {
		item.Port = *input.Port
	}
	if input.DBName != nil {
		item.DBName = *input.DBName
	}
	if input.Username != nil {
		item.Username = *input.Username
	}
	if input.SSLMode != nil {
		item.SSLMode = *input.SSLMode
	}
}

func (h *ResourceHandler) ListDatabases(w http.ResponseWriter, r *http.Request) {
	h.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := tx.Query(ctx,
			"SELECT "+databaseColumns+" FROM db_connections ORDER BY id",
		)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		items := make([]databaseResponse, 0)
		for rows.Next() {
			item, err := scanDatabase(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, rows.Err()
	}, nil)
}

func (h *ResourceHandler) CreateDatabase(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdminUser(w, r); !ok {
		return
	}

	var input databaseInput
	if !decodeUserBody(w, r, &input) {
		return
	}

	item := databaseResponse{Port: 5432, SSLMode: "disable"}
	applyDatabaseInput(&item, input)

	if !validConnection(item) || input.Password == nil {
		resourceFailure(w, badInput("Konfigurasi database tidak valid; password wajib dikirim"))
		return
	}

	encrypted, err := h.encryptPassword(*input.Password)
	if err != nil {
		resourceFailure(w, err)
		return
	}

	h.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return scanDatabase(tx.QueryRow(ctx, `
			INSERT INTO db_connections (
				name, host, port, dbname, username, password_encrypted, sslmode
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING `+databaseColumns,
			item.Name, item.Host, item.Port, item.DBName,
			item.Username, encrypted, item.SSLMode,
		))
	}, nil)
}

func (h *ResourceHandler) UpdateDatabase(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdminUser(w, r); !ok {
		return
	}

	id, err := resourceID(r)
	if err != nil {
		resourceFailure(w, err)
		return
	}

	var input databaseInput
	if !decodeUserBody(w, r, &input) {
		return
	}
	if input.Name == nil && input.Host == nil && input.Port == nil &&
		input.DBName == nil && input.Username == nil &&
		input.Password == nil && input.SSLMode == nil {
		resourceFailure(w, badInput("Kirim minimal satu field"))
		return
	}

	h.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		item, err := scanDatabase(tx.QueryRow(ctx,
			"SELECT "+databaseColumns+
				" FROM db_connections WHERE id = $1 FOR UPDATE", id,
		))
		if err != nil {
			return nil, err
		}

		applyDatabaseInput(&item, input)
		if !validConnection(item) {
			return nil, badInput("Konfigurasi database tidak valid")
		}

		var encrypted []byte
		if input.Password != nil {
			encrypted, err = h.encryptPassword(*input.Password)
			if err != nil {
				return nil, err
			}
		}

		return scanDatabase(tx.QueryRow(ctx, `
			UPDATE db_connections
			SET name = $2, host = $3, port = $4, dbname = $5,
			    username = $6,
			    password_encrypted = COALESCE($7::bytea, password_encrypted),
			    sslmode = $8
			WHERE id = $1
			RETURNING `+databaseColumns,
			id, item.Name, item.Host, item.Port, item.DBName,
			item.Username, encrypted, item.SSLMode,
		))
	}, func() {
		h.manager.Invalidate(id)
	})
}

func (h *ResourceHandler) DeleteDatabase(w http.ResponseWriter, r *http.Request) {
	var deletedID int64

	h.run(w, r, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx) (any, error) {
		id, err := resourceID(r)
		if err != nil {
			return nil, err
		}

		if err := tx.QueryRow(ctx, `
			SELECT id FROM db_connections WHERE id = $1 FOR UPDATE
		`, id).Scan(&deletedID); err != nil {
			return nil, err
		}

		var memberIDs []int64
		if err := tx.QueryRow(ctx, `
			SELECT ARRAY(
				SELECT DISTINCT gm.user_id
				FROM group_members gm
				JOIN group_db_access ga ON ga.group_id = gm.group_id
				WHERE ga.db_connection_id = $1
			)
		`, id).Scan(&memberIDs); err != nil {
			return nil, err
		}
		if err := h.revokeUserSessions(ctx, tx, memberIDs); err != nil {
			return nil, err
		}

		_, err = tx.Exec(ctx,
			"DELETE FROM db_connections WHERE id = $1", id,
		)
		return nil, err
	}, func() {
		h.manager.Invalidate(deletedID)
	})
}

func (h *ResourceHandler) TestDatabase(
	w http.ResponseWriter,
	r *http.Request,
) {
	if _, ok := requireAdminUser(w, r); !ok {
		return
	}

	var input struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		DBName   string `json:"dbname"`
		Username string `json:"username"`
		Password string `json:"password"`
		SSLMode  string `json:"sslmode"`
	}
	input.Port = 5432
	input.SSLMode = "disable"

	if !decodeUserBody(w, r, &input) {
		return
	}

	config := databaseResponse{
		Name:     "connection-test",
		Host:     input.Host,
		Port:     input.Port,
		DBName:   input.DBName,
		Username: input.Username,
		SSLMode:  input.SSLMode,
	}
	if !validConnection(config) {
		resourceFailure(w, badInput("Konfigurasi database tidak valid"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	err := h.manager.Test(ctx, dbmanager.ConnConfig{
		Host:     input.Host,
		Port:     input.Port,
		DBName:   input.DBName,
		Username: input.Username,
		Password: input.Password,
		SSLMode:  input.SSLMode,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "Koneksi database gagal",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{
		"ok": true,
	})
}
