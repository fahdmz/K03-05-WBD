package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/jackc/pgx/v5"
)

func TestListUsersIntegration(t *testing.T) {
	url := testDatabaseURL(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cleanupCancel()
		_ = conn.Close(cleanupCtx)
	}()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cleanupCancel()
		_ = tx.Rollback(cleanupCtx)
	}()

	execSQL := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatalf("execute test SQL: %v", err)
		}
	}

	execSQL("CREATE SCHEMA admin_users_integration_test")
	execSQL("SET LOCAL search_path TO admin_users_integration_test")

	schema, err := os.ReadFile("../../migrations/001_init.sql")
	if err != nil {
		t.Fatalf("read database schema: %v", err)
	}
	execSQL(string(schema))

	handler := NewHandler(tx)

	request := func(user *auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodGet, "/api/admin/users", nil,
		).WithContext(ctx)

		if user != nil {
			req = req.WithContext(auth.WithUser(req.Context(), user))
		}

		rec := httptest.NewRecorder()
		handler.ListUsers(rec, req)
		return rec
	}

	adminUser := &auth.User{
		ID:       1,
		Username: "admin",
		IsAdmin:  true,
	}

	t.Run("empty table returns array", func(t *testing.T) {
		rec := request(adminUser)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body = %s", rec.Code, rec.Body.String())
		}

		if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
			t.Errorf("body = %q, want []", got)
		}
	})

	execSQL(`
		INSERT INTO users (
			id, username, password_hash, is_admin, is_active
		)
		VALUES
			(2, 'member', 'private-member-hash', FALSE, FALSE),
			(1, 'admin', 'private-admin-hash', TRUE, TRUE)
	`)

	t.Run("admin receives stored users without secrets", func(t *testing.T) {
		rec := request(adminUser)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body = %s", rec.Code, rec.Body.String())
		}

		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}

		var got []userResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode users: %v", err)
		}

		want := []userResponse{
			{ID: 1, Username: "admin", IsAdmin: true, IsActive: true},
			{ID: 2, Username: "member", IsAdmin: false, IsActive: false},
		}

		if !reflect.DeepEqual(got, want) {
			t.Errorf("users = %#v, want %#v", got, want)
		}

		var rawUsers []map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &rawUsers); err != nil {
			t.Fatalf("decode response fields: %v", err)
		}

		allowed := map[string]bool{
			"id":       true,
			"username": true,
			"isAdmin":  true,
			"isActive": true,
		}

		for _, user := range rawUsers {
			for field := range user {
				if !allowed[field] {
					t.Errorf("unexpected response field: %s", field)
				}
			}
		}

		for _, secret := range []string{
			"private-member-hash",
			"private-admin-hash",
		} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Error("response exposes password hash")
			}
		}
	})

	t.Run("access denied", func(t *testing.T) {
		tests := []struct {
			name string
			user *auth.User
			code int
		}{
			{
				name: "not logged in",
				code: http.StatusUnauthorized,
			},
			{
				name: "non admin",
				user: &auth.User{
					ID:       2,
					Username: "member",
					IsAdmin:  false,
				},
				code: http.StatusForbidden,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				rec := request(tt.user)

				if rec.Code != tt.code {
					t.Fatalf(
						"status = %d, want %d; body = %s",
						rec.Code, tt.code, rec.Body.String(),
					)
				}

				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode error response: %v", err)
				}

				message, ok := body["error"].(string)
				if !ok || message == "" {
					t.Errorf("expected error message, got %v", body)
				}

				if len(body) != 1 {
					t.Errorf("unexpected fields in error response: %v", body)
				}
			})
		}
	})

	t.Run("database failure returns generic error", func(t *testing.T) {
		execSQL("DROP TABLE users CASCADE")

		rec := request(adminUser)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf(
				"status = %d, want 500; body = %s",
				rec.Code, rec.Body.String(),
			)
		}

		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode error response: %v", err)
		}

		message := body["error"]
		if message != "Gagal mengambil daftar user" &&
			message != "Gagal membaca daftar user" {
			t.Errorf("unexpected error response: %v", body)
		}
	})
}
