package dbmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/authz"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestParsePaginationDefaults(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/databases/1/schemas/public/tables/items/rows", nil)
	page, pageSize, offset, err := parsePagination(r)
	if err != nil {
		t.Fatal(err)
	}
	if page != 1 || pageSize != 50 || offset != 0 {
		t.Fatalf("got page=%d pageSize=%d offset=%d", page, pageSize, offset)
	}
}

func TestParsePaginationBounds(t *testing.T) {
	r := httptest.NewRequest("GET", "/rows?page=3&pageSize=20", nil)
	page, pageSize, offset, err := parsePagination(r)
	if err != nil {
		t.Fatal(err)
	}
	if page != 3 || pageSize != 20 || offset != 40 {
		t.Fatalf("got page=%d pageSize=%d offset=%d", page, pageSize, offset)
	}

	invalid := httptest.NewRequest("GET", "/rows?page=1&pageSize=101", nil)
	if _, _, _, err := parsePagination(invalid); err == nil {
		t.Fatal("expected oversized pageSize to be rejected")
	}
}

type forbiddenViewerAuthorizer struct{}

func (forbiddenViewerAuthorizer) EffectiveLevel(context.Context, int64, int64) (authz.Level, error) {
	return authz.None, nil
}

func (forbiddenViewerAuthorizer) Require(context.Context, int64, int64, authz.Level) error {
	return authz.ErrForbidden
}

func TestViewerRejectsForbiddenAccess(t *testing.T) {
	viewer := NewViewer(nil, Stub{}, forbiddenViewerAuthorizer{})
	mux := http.NewServeMux()
	viewer.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/databases/42/schemas", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: 7, Username: "viewer"}))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusForbidden, response.Body.String())
	}
}

type viewerTestAuthorizer struct {
	err error
}

func (a viewerTestAuthorizer) EffectiveLevel(context.Context, int64, int64) (authz.Level, error) {
	if a.err != nil {
		return authz.None, a.err
	}
	return authz.View, nil
}

func (a viewerTestAuthorizer) Require(context.Context, int64, int64, authz.Level) error {
	return a.err
}

type viewerTestManager struct {
	pool *pgxpool.Pool
	err  error
}

func (m viewerTestManager) Pool(context.Context, int64) (*pgxpool.Pool, error) {
	return m.pool, m.err
}

func (viewerTestManager) Test(context.Context, ConnConfig) error {
	return nil
}

func (viewerTestManager) Invalidate(int64) {}

func viewerTestRequest(method, target string, userID int64) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	return request.WithContext(auth.WithUser(
		request.Context(),
		&auth.User{ID: userID, Username: "viewer"},
	))
}

func TestViewerRejectsTargetConnectionFailure(t *testing.T) {
	viewer := NewViewer(
		nil,
		viewerTestManager{err: fmt.Errorf("connection refused")},
		viewerTestAuthorizer{},
	)
	mux := http.NewServeMux()
	viewer.Register(mux)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, viewerTestRequest(
		http.MethodGet,
		"/api/databases/42/schemas",
		7,
	))

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadGateway, response.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body["error"] != "Koneksi ke database target gagal" {
		t.Fatalf("error = %q, want target connection error", body["error"])
	}
}

func TestViewerIntegration(t *testing.T) {
	platformURL := os.Getenv("VIEWER_TEST_DATABASE_URL")
	targetURL := os.Getenv("VIEWER_TEST_TARGET_URL")
	if platformURL == "" || targetURL == "" {
		t.Skip("set VIEWER_TEST_DATABASE_URL and VIEWER_TEST_TARGET_URL to run PostgreSQL integration tests")
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

	username := fmt.Sprintf("viewer_test_%d", time.Now().UnixNano())
	tableName := fmt.Sprintf("viewer_items_%d", time.Now().UnixNano())
	var userID, dbID int64
	if err := platform.QueryRow(ctx, `
		INSERT INTO users (username, password_hash)
		VALUES ($1, 'test')
		RETURNING id`, username).Scan(&userID); err != nil {
		t.Fatalf("create test user (apply migrations/001_init.sql first): %v", err)
	}
	if err := platform.QueryRow(ctx, `
		INSERT INTO db_connections (name, host, port, dbname, username, password_encrypted)
		VALUES ($1, 'test', 5432, 'test', 'test', $2)
		RETURNING id`, username, []byte{1}).Scan(&dbID); err != nil {
		_, _ = platform.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		t.Fatalf("create test database record: %v", err)
	}
	t.Cleanup(func() {
		_, _ = target.Exec(context.Background(), "DROP TABLE IF EXISTS "+pgx.Identifier{tableName}.Sanitize())
		_, _ = platform.Exec(context.Background(), `DELETE FROM db_connections WHERE id = $1`, dbID)
		_, _ = platform.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	qualifiedTable := pgx.Identifier{"public", tableName}.Sanitize()
	if _, err := target.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			id INTEGER PRIMARY KEY,
			name TEXT NOT NULL
		);
		CREATE INDEX %s ON %s (name);
		INSERT INTO %s (id, name) VALUES (1, 'first'), (2, 'second')`,
		qualifiedTable,
		pgx.Identifier{tableName + "_name_idx"}.Sanitize(),
		qualifiedTable,
		qualifiedTable,
	)); err != nil {
		t.Fatalf("prepare target table: %v", err)
	}

	viewer := NewViewer(platform, viewerTestManager{pool: target}, viewerTestAuthorizer{})
	mux := http.NewServeMux()
	viewer.Register(mux)
	request := func(targetPath string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, viewerTestRequest(http.MethodGet, targetPath, userID))
		return response
	}

	t.Run("lists accessible databases", func(t *testing.T) {
		response := request("/api/databases")
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
		}
		var databases []databaseSummary
		if err := json.Unmarshal(response.Body.Bytes(), &databases); err != nil {
			t.Fatalf("decode databases response: %v", err)
		}
		var found bool
		for _, database := range databases {
			if database.ID == dbID {
				found = true
				if database.Level != "view" {
					t.Fatalf("database level = %q, want view", database.Level)
				}
			}
		}
		if !found {
			t.Fatalf("databases = %#v, want database %d with view access", databases, dbID)
		}
	})

	t.Run("lists schemas", func(t *testing.T) {
		response := request(fmt.Sprintf("/api/databases/%d/schemas", dbID))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
		}
		var schemas []map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &schemas); err != nil {
			t.Fatalf("decode schemas response: %v", err)
		}
		var found bool
		for _, schema := range schemas {
			if schema["name"] == "public" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("schemas = %#v, want public schema", schemas)
		}
	})

	t.Run("lists tables", func(t *testing.T) {
		response := request(fmt.Sprintf("/api/databases/%d/schemas/public/tables", dbID))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
		}
		var tables []map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &tables); err != nil {
			t.Fatalf("decode tables response: %v", err)
		}
		var found bool
		for _, table := range tables {
			if table["name"] == tableName {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("tables = %#v, want %s", tables, tableName)
		}
	})

	t.Run("returns table details", func(t *testing.T) {
		response := request(fmt.Sprintf("/api/databases/%d/schemas/public/tables/%s", dbID, tableName))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
		}
		var details tableDetailsResponse
		if err := json.Unmarshal(response.Body.Bytes(), &details); err != nil {
			t.Fatalf("decode table details response: %v", err)
		}
		if details.Name != tableName || len(details.Columns) != 2 || len(details.Constraints) == 0 || len(details.Indexes) == 0 {
			t.Fatalf("details = %#v, want columns, constraints, and indexes", details)
		}
	})

	t.Run("returns paginated rows", func(t *testing.T) {
		response := request(fmt.Sprintf(
			"/api/databases/%d/schemas/public/tables/%s/rows?page=2&pageSize=1",
			dbID,
			tableName,
		))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
		}
		var rows tableRowsResponse
		if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil {
			t.Fatalf("decode rows response: %v", err)
		}
		if rows.Page != 2 || rows.PageSize != 1 || rows.Total != 2 || len(rows.Rows) != 1 {
			t.Fatalf("rows = %#v, want page 2 with one row from total 2", rows)
		}
	})
}
