package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/authz"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type explainAuthorizer struct{ level authz.Level }

func (a explainAuthorizer) EffectiveLevel(context.Context, int64, int64) (authz.Level, error) {
	return a.level, nil
}
func (a explainAuthorizer) Require(_ context.Context, _, _ int64, minimum authz.Level) error {
	if a.level < minimum {
		return authz.ErrForbidden
	}
	return nil
}

func TestExplainAccessBeforeDatabase(t *testing.T) {
	for _, tc := range []struct {
		name     string
		loggedIn bool
		level    authz.Level
		status   int
	}{
		{"no login", false, authz.Edit, 401},
		{"no access", true, authz.None, 403},
		{"view only", true, authz.View, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(&pgxpool.Pool{}, nil, explainAuthorizer{tc.level})
			mux := http.NewServeMux()
			h.Register(mux)
			req := httptest.NewRequest(http.MethodPost, "/api/databases/1/explain", strings.NewReader(`{"sql":"SELECT 1"}`))
			if tc.loggedIn {
				req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: 1}))
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("HTTP %d want %d", rec.Code, tc.status)
			}
		})
	}
}

func TestExplainErrors(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
	}{
		{"57014", 504}, {"55P03", 504}, {"42601", 400}, {"0A000", 400},
		{"25006", 400}, {"42501", 403}, {"42P01", 400}, {"22012", 400}, {"08006", 502},
	} {
		t.Run(tc.code, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeExplainError(rec, context.Background(), &pgconn.PgError{Code: tc.code, Message: "private credentials"})
			if rec.Code != tc.status || strings.Contains(rec.Body.String(), "private credentials") {
				t.Fatalf("unexpected response: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestExplainIntegration(t *testing.T) {
	ctx := context.Background()
	platform := setupPlatformDB(t, testDatabaseURL())
	target, schema := setupTargetDB(t, testDatabaseURL())
	table := fmt.Sprintf("explain_%d", time.Now().UnixNano())
	qualified := pgx.Identifier{schema, table}.Sanitize()
	if _, err := target.Exec(ctx, "CREATE TABLE "+qualified+" (id integer PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dropTargetTable(t, target, schema, table) })
	var dbID int64
	err := platform.QueryRow(ctx, `INSERT INTO db_connections (name, host, port, dbname, username, password_encrypted) VALUES ($1, 'test', 5432, 'test', 'test', $2) RETURNING id`, table, []byte{1}).Scan(&dbID)
	if err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(platform, testManager{pool: target}, explainAuthorizer{authz.Edit})
	call := func(h *Handler, body string, loggedIn bool) *httptest.ResponseRecorder {
		t.Helper()
		mux := http.NewServeMux()
		h.Register(mux)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/databases/%d/explain", dbID), strings.NewReader(body))
		if loggedIn {
			req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: 1, Username: "explain"}))
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	sqlBody := func(sql string) string { b, _ := json.Marshal(map[string]string{"sql": sql}); return string(b) }

	for _, tc := range []struct {
		name, sql string
		status    int
	}{
		{"select plan", "SELECT * FROM " + qualified, 200},
		{"semicolon in literal", "SELECT ';'::text", 200},
		{"insert is only planned", "INSERT INTO " + qualified + " VALUES (1)", 200},
		{"multiple statements rejected", "SELECT 1; INSERT INTO " + qualified + " VALUES (2)", 400},
		{"DDL unsupported", "CREATE TABLE should_not_exist (id integer)", 400},
		{"nested analyze rejected", "EXPLAIN ANALYZE SELECT 1", 400},
		{"invalid SQL", "SELECT FROM", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(handler, sqlBody(tc.sql), true)
			if rec.Code != tc.status {
				t.Fatalf("HTTP %d want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			wantMessage := map[string]string{
				"multiple statements rejected": "Hanya satu statement SQL",
				"DDL unsupported":              "Statement ini tidak dapat dianalisis",
				"nested analyze rejected":      "Statement ini tidak dapat dianalisis",
				"invalid SQL":                  "Sintaks SQL tidak valid",
			}[tc.name]
			if wantMessage != "" && !strings.Contains(rec.Body.String(), wantMessage) {
				t.Fatalf("expected error containing %q: %s", wantMessage, rec.Body.String())
			}
			if tc.status == 200 {
				var result struct {
					DatabaseID int64 `json:"databaseId"`
					Plan       []struct {
						Plan map[string]any `json:"Plan"`
					} `json:"plan"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.DatabaseID != dbID || len(result.Plan) != 1 || result.Plan[0].Plan["Node Type"] == nil {
					t.Fatalf("invalid plan: %s", rec.Body.String())
				}
			}
		})
	}
	for _, body := range []string{`{}`, `{"sql":" "}`, `{"sql":"SELECT 1","extra":true}`, `{"sql":"SELECT 1"} {}`, `null`} {
		t.Run("invalid body "+body, func(t *testing.T) {
			if rec := call(handler, body, true); rec.Code != 400 {
				t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	t.Run("unauthenticated", func(t *testing.T) {
		if rec := call(handler, sqlBody("SELECT 1"), false); rec.Code != 401 {
			t.Fatalf("HTTP %d", rec.Code)
		}
	})
	t.Run("view cannot explain", func(t *testing.T) {
		denied := NewHandler(platform, testManager{pool: target}, explainAuthorizer{authz.View})
		if rec := call(denied, sqlBody("SELECT 1"), true); rec.Code != 403 {
			t.Fatalf("HTTP %d", rec.Code)
		}
	})
	t.Run("connection failure", func(t *testing.T) {
		broken := NewHandler(platform, testManager{err: errors.New("private credentials")}, explainAuthorizer{authz.Edit})
		rec := call(broken, sqlBody("SELECT 1"), true)
		if rec.Code != 502 || strings.Contains(rec.Body.String(), "private credentials") {
			t.Fatalf("unexpected response: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("lock timeout", func(t *testing.T) {
		tx, err := target.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "LOCK TABLE "+qualified+" IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatal(err)
		}
		rec := call(handler, sqlBody("SELECT * FROM "+qualified), true)
		if rec.Code != 504 {
			t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
		}
	})
	var count int
	if err := target.QueryRow(ctx, "SELECT count(*) FROM "+qualified).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("EXPLAIN changed data: %d rows", count)
	}
	if err := platform.QueryRow(ctx, "SELECT count(*) FROM migrations WHERE db_connection_id=$1", dbID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("EXPLAIN created migration history: %d rows", count)
	}
}
