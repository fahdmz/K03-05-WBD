package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/authz"
	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"
	"github.com/jackc/pgx/v5/pgxpool"
)

type testAuthorizer struct{ err error }

func (a testAuthorizer) EffectiveLevel(context.Context, int64, int64) (authz.Level, error) {
	if a.err != nil {
		return authz.None, a.err
	}
	return authz.Edit, nil
}

func (a testAuthorizer) Require(context.Context, int64, int64, authz.Level) error { return a.err }

type testManager struct {
	pool *pgxpool.Pool
	err  error
}

func (m testManager) Pool(context.Context, int64) (*pgxpool.Pool, error) { return m.pool, m.err }
func (testManager) Test(context.Context, dbmanager.ConnConfig) error     { return nil }
func (testManager) Invalidate(int64)                                     {}

func TestDecodeMigrationInput(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"  add column  ","sql":"ALTER TABLE items ADD COLUMN name TEXT;"}`))
	response := httptest.NewRecorder()
	input, ok := decodeMigrationInput(response, request)
	if !ok {
		t.Fatalf("valid input rejected: %s", response.Body.String())
	}
	if input.Title != "add column" || input.SQL != "ALTER TABLE items ADD COLUMN name TEXT;" {
		t.Fatalf("unexpected normalized input: %#v", input)
	}

	for _, body := range []string{
		`{"title":"","sql":"SELECT 1"}`,
		`{"title":"valid","sql":"  "}`,
		`{"title":"valid","sql":"SELECT 1","unexpected":true}`,
		`{"title":"valid","sql":"SELECT 1"} {}`,
		`{"title":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sql":"SELECT 1"}`,
	} {
		t.Run(body, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			rec := httptest.NewRecorder()
			if _, ok := decodeMigrationInput(rec, req); ok {
				t.Fatal("invalid input was accepted")
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestMigrationIntegration(t *testing.T) {
	platformURL := os.Getenv("MIGRATION_TEST_DATABASE_URL")
	targetURL := os.Getenv("MIGRATION_TEST_TARGET_URL")
	if platformURL == "" || targetURL == "" {
		t.Skip("set MIGRATION_TEST_DATABASE_URL and MIGRATION_TEST_TARGET_URL to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	platform, err := pgxpool.New(ctx, platformURL)
	if err != nil {
		t.Fatalf("connect platform test database: %v", err)
	}
	defer platform.Close()
	target, err := pgxpool.New(ctx, targetURL)
	if err != nil {
		t.Fatalf("connect target test database: %v", err)
	}
	defer target.Close()
	if err := platform.Ping(ctx); err != nil {
		t.Fatalf("ping platform test database: %v", err)
	}
	if err := target.Ping(ctx); err != nil {
		t.Fatalf("ping target test database: %v", err)
	}

	username := fmt.Sprintf("migration_test_%d", time.Now().UnixNano())
	var userID, dbID int64
	if err := platform.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1, 'test') RETURNING id`, username).Scan(&userID); err != nil {
		t.Fatalf("create test user (apply migrations/001_init.sql first): %v", err)
	}
	if err := platform.QueryRow(ctx, `
		INSERT INTO db_connections (name, host, port, dbname, username, password_encrypted)
		VALUES ($1, 'test', 5432, 'test', 'test', $2) RETURNING id`, username, []byte{1}).Scan(&dbID); err != nil {
		_, _ = platform.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		t.Fatalf("create test database record: %v", err)
	}
	t.Cleanup(func() {
		_, _ = platform.Exec(context.Background(), `DELETE FROM db_connections WHERE id = $1`, dbID)
		_, _ = platform.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	handler := NewHandler(platform, testManager{pool: target}, testAuthorizer{})
	mux := http.NewServeMux()
	handler.Register(mux)
	assertExecute := func(t *testing.T, sql string) (Migration, int) {
		t.Helper()
		var migrationID int64
		if err := platform.QueryRow(ctx, `
			INSERT INTO migrations (db_connection_id, title, sql, status, created_by)
			VALUES ($1, 'integration test', $2, 'draft', $3) RETURNING id`, dbID, sql, userID).Scan(&migrationID); err != nil {
			t.Fatalf("create migration fixture: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/migrations/%d/execute", migrationID), nil)
		req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: userID, Username: username}))
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		var item Migration
		if err := json.Unmarshal(response.Body.Bytes(), &item); err != nil {
			t.Fatalf("decode execution response (HTTP %d): %v; body=%s", response.Code, err, response.Body.String())
		}
		return item, response.Code
	}

	t.Run("successful migration commits and receives next version", func(t *testing.T) {
		tableName := fmt.Sprintf("migration_ok_%d", time.Now().UnixNano())
		t.Cleanup(func() { _, _ = target.Exec(context.Background(), "DROP TABLE IF EXISTS "+tableName) })
		item, status := assertExecute(t, "CREATE TABLE "+tableName+" (id INTEGER)")
		if status != http.StatusOK || item.Status != "success" || item.Version == nil || *item.Version != 1 {
			t.Fatalf("unexpected success result: HTTP %d, migration %#v", status, item)
		}
		update := httptest.NewRequest(
			http.MethodPatch,
			fmt.Sprintf("/api/migrations/%d", item.ID),
			strings.NewReader(`{"title":"changed","sql":"SELECT 1"}`),
		)
		update = update.WithContext(auth.WithUser(update.Context(), &auth.User{ID: userID, Username: username}))
		updateResponse := httptest.NewRecorder()
		mux.ServeHTTP(updateResponse, update)
		if updateResponse.Code != http.StatusBadRequest {
			t.Fatalf("update status = %d, want %d; body=%s", updateResponse.Code, http.StatusBadRequest, updateResponse.Body.String())
		}
		var exists bool
		if err := target.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+tableName).Scan(&exists); err != nil || !exists {
			t.Fatalf("migration DDL not committed (exists=%t, err=%v)", exists, err)
		}
	})

	t.Run("failed migration rolls back and records failure", func(t *testing.T) {
		tableName := fmt.Sprintf("migration_rollback_%d", time.Now().UnixNano())
		t.Cleanup(func() { _, _ = target.Exec(context.Background(), "DROP TABLE IF EXISTS "+tableName) })
		item, status := assertExecute(t, "CREATE TABLE "+tableName+" (id INTEGER); SELECT 1 / 0")
		if status != http.StatusOK || item.Status != "failed" || item.Version != nil || item.ErrorMessage == nil {
			t.Fatalf("unexpected failure result: HTTP %d, migration %#v", status, item)
		}
		var exists bool
		if err := target.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+tableName).Scan(&exists); err != nil || exists {
			t.Fatalf("failed migration left DDL committed (exists=%t, err=%v)", exists, err)
		}
	})

	t.Run("view-only user cannot create or execute migrations", func(t *testing.T) {
		denied := NewHandler(platform, testManager{pool: target}, testAuthorizer{err: authz.ErrForbidden})
		deniedMux := http.NewServeMux()
		denied.Register(deniedMux)
		userContext := func(req *http.Request) *http.Request {
			return req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: userID, Username: username}))
		}
		create := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/databases/%d/migrations", dbID), strings.NewReader(`{"title":"denied","sql":"SELECT 1"}`))
		createResponse := httptest.NewRecorder()
		deniedMux.ServeHTTP(createResponse, userContext(create))
		if createResponse.Code != http.StatusForbidden {
			t.Fatalf("create status = %d, want 403; body=%s", createResponse.Code, createResponse.Body.String())
		}

		var migrationID int64
		if err := platform.QueryRow(ctx, `
			INSERT INTO migrations (db_connection_id, title, sql, status, created_by)
			VALUES ($1, 'denied execute', 'SELECT 1', 'draft', $2) RETURNING id`, dbID, userID).Scan(&migrationID); err != nil {
			t.Fatalf("create migration fixture: %v", err)
		}
		execute := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/migrations/%d/execute", migrationID), nil)
		executeResponse := httptest.NewRecorder()
		deniedMux.ServeHTTP(executeResponse, userContext(execute))
		if executeResponse.Code != http.StatusForbidden {
			t.Fatalf("execute status = %d, want 403; body=%s", executeResponse.Code, executeResponse.Body.String())
		}
	})

}
