package admin

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type adminTestManager struct {
	dbmanager.Stub
	testErr     error
	testCalls   int
	lastConfig  dbmanager.ConnConfig
	invalidated []int64
}

func (m *adminTestManager) Test(
	ctx context.Context,
	cfg dbmanager.ConnConfig,
) error {
	m.testCalls++
	m.lastConfig = cfg
	return m.testErr
}

func (m *adminTestManager) Invalidate(id int64) {
	m.invalidated = append(m.invalidated, id)
}

func adminTestAEAD(t *testing.T) cipher.AEAD {
	t.Helper()

	block, err := aes.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return aead
}

func adminTestRequest(
	handler http.HandlerFunc,
	method, id, body string,
	user *auth.User,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if id != "" {
		req.SetPathValue("id", id)
	}
	if user != nil {
		req = req.WithContext(auth.WithUser(req.Context(), user))
	}

	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func adminTestStatus(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	want int,
) {
	t.Helper()

	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body = %s",
			rec.Code, want, rec.Body.String())
	}

	if want == http.StatusNoContent {
		if rec.Body.Len() != 0 {
			t.Fatal("204 response must have an empty body")
		}
		return
	}

	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected Content-Type: %s", rec.Header().Get("Content-Type"))
	}

	if want >= 400 {
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		message, ok := body["error"].(string)
		if !ok || message == "" || len(body) != 1 {
			t.Fatalf("unexpected error body: %v", body)
		}
	}
}

func adminTestDecode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var result T
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, rec.Body.String())
	}
	return result
}

func adminTestNoSecrets(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	var body any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	var inspect func(any)
	inspect = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				if strings.Contains(strings.ToLower(key), "password") {
					t.Errorf("response contains sensitive field: %s", key)
				}
				inspect(child)
			}
		case []any:
			for _, child := range v {
				inspect(child)
			}
		}
	}
	inspect(body)
}

type adminIntegrationEnv struct {
	db      *pgxpool.Pool
	users   *Handler
	res     *ResourceHandler
	manager *adminTestManager
}

// Each test uses its own schema because resource handlers commit transactions.
func newAdminIntegrationEnv(t *testing.T) *adminIntegrationEnv {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL belum diatur")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	control, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.Close)

	token := make([]byte, 12)
	if _, err := rand.Read(token); err != nil {
		t.Fatal(err)
	}

	schemaName := "admin_test_" + hex.EncodeToString(token)
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()

	if _, err := control.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(), 10*time.Second,
		)
		defer cleanupCancel()

		if _, err := control.Exec(
			cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE",
		); err != nil {
			t.Errorf("cleanup test schema: %v", err)
		}
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName

	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	schema, err := os.ReadFile("../../migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, string(schema)); err != nil {
		t.Fatal(err)
	}

	manager := &adminTestManager{}
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	res, err := NewResourceHandler(db, manager, key)
	if err != nil {
		t.Fatal(err)
	}

	return &adminIntegrationEnv{
		db: db, users: NewHandler(db), res: res, manager: manager,
	}
}

func (e *adminIntegrationEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := e.db.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("fixture SQL: %v", err)
	}
}

func (e *adminIntegrationEnv) scan(
	t *testing.T,
	sql string,
	args []any,
	dest ...any,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := e.db.QueryRow(ctx, sql, args...).Scan(dest...); err != nil {
		t.Fatalf("verification query: %v", err)
	}
}

func (e *adminIntegrationEnv) count(
	t *testing.T,
	sql string,
	want int,
	args ...any,
) {
	t.Helper()
	var got int
	e.scan(t, sql, args, &got)
	if got != want {
		t.Fatalf("count = %d, want %d", got, want)
	}
}

func (e *adminIntegrationEnv) call(
	t *testing.T,
	handler http.HandlerFunc,
	method, id, body string,
	want int,
) *httptest.ResponseRecorder {
	t.Helper()

	rec := adminTestRequest(handler, method, id, body, &auth.User{
		ID: 10, Username: "admin", IsAdmin: true,
	})
	adminTestStatus(t, rec, want)
	return rec
}

func (e *adminIntegrationEnv) seed(t *testing.T) {
	t.Helper()
	e.exec(t, `
		INSERT INTO users (id, username, password_hash, is_admin)
		VALUES
			(10, 'admin', 'fixture', TRUE),
			(20, 'member', 'fixture', FALSE),
			(30, 'other', 'fixture', FALSE);

		INSERT INTO groups (id, name, description)
		VALUES (10, 'team', 'original');

		INSERT INTO db_connections (
			id, name, host, dbname, username, password_encrypted
		)
		VALUES (10, 'fixture-db', 'localhost', 'shop', 'target', '\x00');
	`)
}

func (e *adminIntegrationEnv) session(t *testing.T, userID int64, token string) {
	t.Helper()
	e.exec(t, `
		INSERT INTO sessions (
			user_id, current_token_hash, csrf_token, absolute_expires_at
		)
		VALUES ($1, $2, 'test-csrf', NOW() + INTERVAL '1 hour')
	`, userID, token)
}