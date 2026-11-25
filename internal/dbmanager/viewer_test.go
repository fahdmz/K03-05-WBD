package dbmanager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	platformURL := envOr("VIEWER_TEST_DATABASE_URL", testDatabaseURL())
	targetURL := envOr("VIEWER_TEST_TARGET_URL", testDatabaseURL())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	platform := setupPlatformDB(t, platformURL)
	target, targetSchema := setupTargetDB(t, targetURL)

	username := fmt.Sprintf("viewer_test_%d", time.Now().UnixNano())
	tableName := fmt.Sprintf("viewer_items_%d", time.Now().UnixNano())
	var userID, dbID int64
	if err := platform.QueryRow(ctx, `
		INSERT INTO users (username, password_hash)
		VALUES ($1, 'test')
		RETURNING id`, username).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	if err := platform.QueryRow(ctx, `
		INSERT INTO db_connections (name, host, port, dbname, username, password_encrypted)
		VALUES ($1, 'test', 5432, 'test', 'test', $2)
		RETURNING id`, username, []byte{1}).Scan(&dbID); err != nil {
		t.Fatalf("create test database record: %v", err)
	}

	t.Cleanup(func() { dropTargetTable(t, target, targetSchema, tableName) })
	qualifiedTable := pgx.Identifier{targetSchema, tableName}.Sanitize()
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
			if schema["name"] == targetSchema {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("schemas = %#v, want %s schema", schemas, targetSchema)
		}
	})

	t.Run("lists tables", func(t *testing.T) {
		response := request(fmt.Sprintf("/api/databases/%d/schemas/%s/tables", dbID, targetSchema))
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
		response := request(fmt.Sprintf("/api/databases/%d/schemas/%s/tables/%s", dbID, targetSchema, tableName))
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
			"/api/databases/%d/schemas/%s/tables/%s/rows?page=2&pageSize=1",
			dbID,
			targetSchema,
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

// tempTargetDB bikin database target acak (di-drop pas cleanup) dan balikin pool-nya.
func tempTargetDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	base := testDatabaseURL()
	adminCfg, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	admin := connectOrSkip(t, adminCfg)

	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	name := "viewer_" + hex.EncodeToString(b)
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(context.Background(), "CREATE DATABASE "+ident); err != nil {
		admin.Close()
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		defer admin.Close()
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})

	cfg, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool := connectOrSkip(t, cfg)
	t.Cleanup(pool.Close)
	return pool
}

func TestViewerHidesPlatformSchemaIntegration(t *testing.T) {
	target := tempTargetDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := target.Exec(ctx, `
		CREATE SCHEMA _platform;
		CREATE TABLE _platform.executions (execution_id UUID PRIMARY KEY);
		CREATE SCHEMA shop;
		CREATE TABLE shop.executions (id INT PRIMARY KEY);
		INSERT INTO shop.executions VALUES (1)`); err != nil {
		t.Fatalf("prepare target: %v", err)
	}

	viewer := NewViewer(nil, viewerTestManager{pool: target}, viewerTestAuthorizer{})
	mux := http.NewServeMux()
	viewer.Register(mux)
	get := func(path string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, viewerTestRequest(http.MethodGet, path, 7))
		return res
	}

	res := get("/api/databases/1/schemas")
	if res.Code != http.StatusOK {
		t.Fatalf("schemas status = %d; body=%s", res.Code, res.Body.String())
	}
	var schemas []map[string]string
	if err := json.Unmarshal(res.Body.Bytes(), &schemas); err != nil {
		t.Fatal(err)
	}
	var hasShop bool
	for _, s := range schemas {
		if s["name"] == "_platform" {
			t.Fatalf("schemas = %#v, _platform harus disembunyikan", schemas)
		}
		hasShop = hasShop || s["name"] == "shop"
	}
	if !hasShop {
		t.Fatalf("schemas = %#v, want shop", schemas)
	}

	for _, path := range []string{
		"/api/databases/1/schemas/_platform/tables",
		"/api/databases/1/schemas/_platform/tables/executions",
		"/api/databases/1/schemas/_platform/tables/executions/rows",
	} {
		if res := get(path); res.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404; body=%s", path, res.Code, res.Body.String())
		}
	}

	// tabel dengan nama sama di schema biasa tetap kebaca
	for _, path := range []string{
		"/api/databases/1/schemas/shop/tables",
		"/api/databases/1/schemas/shop/tables/executions",
		"/api/databases/1/schemas/shop/tables/executions/rows",
	} {
		if res := get(path); res.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; body=%s", path, res.Code, res.Body.String())
		}
	}
}
