package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/authz"
	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"
	"github.com/jackc/pgx/v5"
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

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

func TestDecodeMigrationInput(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{
		"title":"  add column  ",
		"sqlUp":"ALTER TABLE items ADD COLUMN name TEXT;",
		"sqlDown":"ALTER TABLE items DROP COLUMN name;"
	}`))
	response := httptest.NewRecorder()
	input, ok := decodeMigrationInput(response, request)
	if !ok {
		t.Fatalf("valid input rejected: %s", response.Body.String())
	}
	if input.Title != "add column" || input.Mode != "normal" || input.SQLUp != "ALTER TABLE items ADD COLUMN name TEXT;" {
		t.Fatalf("unexpected normalized input: %#v", input)
	}

	for _, body := range []string{
		`{"title":"","sqlUp":"SELECT 1"}`,
		`{"title":"valid","sqlUp":"  "}`,
		`{"title":"valid","sqlUp":"SELECT 1","unexpected":true}`,
		`{"title":"valid","sqlUp":"SELECT 1"} {}`,
		`{"title":"valid","sqlUp":"SELECT 1","mode":"invalid"}`,
		`{"title":"valid","sqlUp":"SELECT 1","mode":"batch"}`,
		`{"title":"valid","sqlUp":"SELECT 1","mode":"batch","batchTable":"t","batchColumn":"id","batchSize":0}`,
		`{"title":"valid","sqlUp":"SELECT 1","mode":"batch","batchTable":"t","batchColumn":"id","batchSize":100001}`,
		`{"title":"` + strings.Repeat("a", 201) + `","sqlUp":"SELECT 1"}`,
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

func TestValidateStructureUnit(t *testing.T) {
	cases := []struct {
		name      string
		spec      RevisionSpec
		wantValid bool
	}{
		{
			name: "valid normal with sqlDown",
			spec: RevisionSpec{
				Mode:    "normal",
				SQLUp:   "CREATE TABLE a (id INT);",
				SQLDown: strPtr("DROP TABLE a;"),
			},
			wantValid: true,
		},
		{
			name: "valid irreversible with reason",
			spec: RevisionSpec{
				Mode:               "normal",
				SQLUp:              "DELETE FROM logs;",
				Irreversible:       true,
				IrreversibleReason: strPtr("Data tidak dapat dipulihkan"),
			},
			wantValid: true,
		},
		{
			name: "missing sqlDown when reversible",
			spec: RevisionSpec{
				Mode:    "normal",
				SQLUp:   "CREATE TABLE a (id INT);",
				SQLDown: strPtr("   "),
			},
			wantValid: false,
		},
		{
			name: "missing reason when irreversible",
			spec: RevisionSpec{
				Mode:               "normal",
				SQLUp:              "DELETE FROM logs;",
				Irreversible:       true,
				IrreversibleReason: strPtr(" "),
			},
			wantValid: false,
		},
		{
			name: "batch missing placeholders in sqlUp",
			spec: RevisionSpec{
				Mode:        "batch",
				SQLUp:       "UPDATE orders SET x = 1;",
				SQLDown:     strPtr("UPDATE orders SET x = 0 WHERE id > {{lo}} AND id <= {{hi}};"),
				BatchTable:  strPtr("orders"),
				BatchColumn: strPtr("id"),
				BatchSize:   intPtr(1000),
			},
			wantValid: false,
		},
		{
			name: "batch missing placeholders in sqlDown",
			spec: RevisionSpec{
				Mode:        "batch",
				SQLUp:       "UPDATE orders SET x = 1 WHERE id > {{lo}} AND id <= {{hi}};",
				SQLDown:     strPtr("UPDATE orders SET x = 0;"),
				BatchTable:  strPtr("orders"),
				BatchColumn: strPtr("id"),
				BatchSize:   intPtr(1000),
			},
			wantValid: false,
		},
		{
			name: "valid batch",
			spec: RevisionSpec{
				Mode:        "batch",
				SQLUp:       "UPDATE orders SET x = 1 WHERE id > {{lo}} AND id <= {{hi}};",
				SQLDown:     strPtr("UPDATE orders SET x = 0 WHERE id > {{lo}} AND id <= {{hi}};"),
				BatchTable:  strPtr("orders"),
				BatchColumn: strPtr("id"),
				BatchSize:   intPtr(1000),
			},
			wantValid: true,
		},
		{
			name: "reject COMMIT in sqlUp",
			spec: RevisionSpec{
				Mode:    "normal",
				SQLUp:   "CREATE TABLE a (id INT); COMMIT;",
				SQLDown: strPtr("DROP TABLE a;"),
			},
			wantValid: false,
		},
		{
			name: "reject BEGIN and START TRANSACTION",
			spec: RevisionSpec{
				Mode:    "normal",
				SQLUp:   "START TRANSACTION; CREATE TABLE a (id INT);",
				SQLDown: strPtr("DROP TABLE a;"),
			},
			wantValid: false,
		},
		{
			name: "reject SAVEPOINT in sqlDown",
			spec: RevisionSpec{
				Mode:    "normal",
				SQLUp:   "CREATE TABLE a (id INT);",
				SQLDown: strPtr("SAVEPOINT sp1; DROP TABLE a;"),
			},
			wantValid: false,
		},
		{
			name: "allow BEGIN END inside dollar-quoted function",
			spec: RevisionSpec{
				Mode:    "normal",
				SQLUp:   "CREATE FUNCTION f() RETURNS void AS $$ BEGIN PERFORM 1; END; $$ LANGUAGE plpgsql;",
				SQLDown: strPtr("DROP FUNCTION f();"),
			},
			wantValid: true,
		},
		{
			name: "reject dollar-identifier bypass with COMMIT",
			spec: RevisionSpec{
				Mode:    "normal",
				SQLUp:   "SELECT 1 AS a$$x$; COMMIT; DROP TABLE t; SELECT $x$",
				SQLDown: strPtr("SELECT 1;"),
			},
			wantValid: false,
		},
		{
			name: "reject multi-statement in batch mode",
			spec: RevisionSpec{
				Mode:        "batch",
				SQLUp:       "UPDATE orders SET x = 1 WHERE id > {{lo}} AND id <= {{hi}}; SELECT 1;",
				SQLDown:     strPtr("UPDATE orders SET x = 0 WHERE id > {{lo}} AND id <= {{hi}};"),
				BatchTable:  strPtr("orders"),
				BatchColumn: strPtr("id"),
				BatchSize:   intPtr(1000),
			},
			wantValid: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := ValidateStructure(tc.spec)
			if res.Valid != tc.wantValid {
				t.Fatalf("Valid = %v, want %v; errors = %v", res.Valid, tc.wantValid, res.Errors)
			}
		})
	}
}

func TestMigrationIntegration(t *testing.T) {
	platformURL := envOr("MIGRATION_TEST_DATABASE_URL", testDatabaseURL())
	targetURL := envOr("MIGRATION_TEST_TARGET_URL", testDatabaseURL())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	platform := setupPlatformDB(t, platformURL)
	target, targetSchema := setupTargetDB(t, targetURL)

	username := fmt.Sprintf("migration_test_%d", time.Now().UnixNano())
	var userID, dbID int64
	if err := platform.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1, 'test') RETURNING id`, username).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	if err := platform.QueryRow(ctx, `
		INSERT INTO db_connections (name, host, port, dbname, username, password_encrypted)
		VALUES ($1, 'test', 5432, 'test', 'test', $2) RETURNING id`, username, []byte{1}).Scan(&dbID); err != nil {
		t.Fatalf("create test database record: %v", err)
	}
	tableExists := func(t *testing.T, tableName string) bool {
		t.Helper()
		var exists bool
		if err := target.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, pgx.Identifier{targetSchema, tableName}.Sanitize()).Scan(&exists); err != nil {
			t.Fatalf("check table %s: %v", tableName, err)
		}
		return exists
	}

	handler := NewHandler(platform, testManager{pool: target}, testAuthorizer{})
	mux := http.NewServeMux()
	handler.Register(mux)

	asUser := func(req *http.Request) *http.Request {
		return req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: userID, Username: username}))
	}

	t.Run("create, validate dry run rollback, submit, and immutable revisions", func(t *testing.T) {
		tableName := fmt.Sprintf("mig_dryrun_%d", time.Now().UnixNano())
		t.Cleanup(func() { dropTargetTable(t, target, targetSchema, tableName) })

		// 1. Create draft tanpa sqlDown -> boleh disimpan sebagai draft.
		createBody := fmt.Sprintf(`{"title":"create %s","sqlUp":"CREATE TABLE %s (id INT);"}`, tableName, tableName)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/databases/%d/migrations", dbID), strings.NewReader(createBody))))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
		}
		var detail MigrationDetail
		if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
			t.Fatalf("decode create: %v", err)
		}
		if detail.CurrentRevision == nil || detail.CurrentRevision.RevisionNo != 1 || detail.CurrentRevision.Status != "draft" {
			t.Fatalf("unexpected initial revision: %#v", detail.CurrentRevision)
		}

		// 2. Validate gagal karena belum ada sqlDown dan tidak irreversible.
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/migrations/%d/validate", detail.ID), nil)))
		if rec.Code != http.StatusOK {
			t.Fatalf("validate status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var valRes ValidationResult
		if err := json.Unmarshal(rec.Body.Bytes(), &valRes); err != nil {
			t.Fatalf("decode validate: %v", err)
		}
		if valRes.Valid || len(valRes.Errors) == 0 {
			t.Fatalf("expected structure validation failure, got %#v", valRes)
		}

		// 3. Submit juga ditolak karena validasi gagal.
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/migrations/%d/submit", detail.ID), nil)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("submit invalid status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}

		// 4. Update draft dengan SQL invalid -> dry run menangkap error SQL.
		badPatch := fmt.Sprintf(`{"title":"create %s","sqlUp":"CREATE TABLE %s (id INT); SELECT 1 / 0;","sqlDown":"DROP TABLE %s;"}`, tableName, tableName, tableName)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/migrations/%d", detail.ID), strings.NewReader(badPatch))))
		if rec.Code != http.StatusOK {
			t.Fatalf("patch bad sql status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/migrations/%d/validate", detail.ID), nil)))
		if err := json.Unmarshal(rec.Body.Bytes(), &valRes); err != nil {
			t.Fatalf("decode dry run error: %v", err)
		}
		if valRes.Valid || len(valRes.Errors) == 0 {
			t.Fatalf("expected dry run SQL error, got %#v", valRes)
		}
		if tableExists(t, tableName) {
			t.Fatalf("dry run committed table %s on error", tableName)
		}

		// 5. Update draft dengan SQL valid -> validate lulus & tetap di-rollback.
		goodPatch := fmt.Sprintf(`{"title":"create %s","sqlUp":"CREATE TABLE %s (id INT);","sqlDown":"DROP TABLE %s;"}`, tableName, tableName, tableName)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/migrations/%d", detail.ID), strings.NewReader(goodPatch))))
		if rec.Code != http.StatusOK {
			t.Fatalf("patch good sql status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/migrations/%d/validate", detail.ID), nil)))
		if err := json.Unmarshal(rec.Body.Bytes(), &valRes); err != nil {
			t.Fatalf("decode dry run ok: %v", err)
		}
		if !valRes.Valid || len(valRes.Errors) != 0 {
			t.Fatalf("expected valid dry run, got %#v", valRes)
		}
		if tableExists(t, tableName) {
			t.Fatalf("dry run should have rolled back %s", tableName)
		}

		// 6. Submit -> revisi 1 dibekukan, change request dibuat.
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/migrations/%d/submit", detail.ID), nil)))
		if rec.Code != http.StatusCreated {
			t.Fatalf("submit status = %d, want 201; body=%s", rec.Code, rec.Body.String())
		}
		var submitResp struct {
			ChangeRequestID int64           `json:"changeRequestId"`
			Migration       MigrationDetail `json:"migration"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &submitResp); err != nil {
			t.Fatalf("decode submit: %v", err)
		}
		if submitResp.ChangeRequestID <= 0 || submitResp.Migration.CurrentRevision.Status != "submitted" {
			t.Fatalf("unexpected submit response: %#v", submitResp)
		}
		if len(submitResp.Migration.ChangeRequests) != 1 || submitResp.Migration.ChangeRequests[0].Status != "pending_review" {
			t.Fatalf("unexpected change requests: %#v", submitResp.Migration.ChangeRequests)
		}

		// 7. PATCH setelah submitted (belum pernah dieksekusi) -> buat revisi 2, revisi 1 tetap immutable.
		rev2Patch := fmt.Sprintf(`{"title":"create %s v2","sqlUp":"CREATE TABLE %s (id INT, name TEXT);","sqlDown":"DROP TABLE %s;"}`, tableName, tableName, tableName)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/migrations/%d", detail.ID), strings.NewReader(rev2Patch))))
		if rec.Code != http.StatusOK {
			t.Fatalf("patch rev2 status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
			t.Fatalf("decode rev2: %v", err)
		}
		if len(detail.Revisions) != 2 || detail.Revisions[0].Status != "submitted" || detail.Revisions[1].RevisionNo != 2 || detail.Revisions[1].Status != "draft" {
			t.Fatalf("unexpected revisions after patch: %#v", detail.Revisions)
		}

		// 8. Jika migration sudah pernah dieksekusi (version terisi), PATCH ditolak.
		if _, err := platform.Exec(ctx, `UPDATE migration_revisions SET status = 'submitted' WHERE id = $1`, detail.CurrentRevision.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := platform.Exec(ctx, `UPDATE migrations SET version = 1, is_applied = TRUE WHERE id = $1`, detail.ID); err != nil {
			t.Fatal(err)
		}
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/migrations/%d", detail.ID), strings.NewReader(rev2Patch))))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("patch executed migration status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("batch validation checks unique index on target", func(t *testing.T) {
		tableName := fmt.Sprintf("mig_batch_%d", time.Now().UnixNano())
		t.Cleanup(func() { dropTargetTable(t, target, targetSchema, tableName) })
		if _, err := target.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s (id BIGINT PRIMARY KEY, val INT)`, pgx.Identifier{targetSchema, tableName}.Sanitize())); err != nil {
			t.Fatalf("create batch table: %v", err)
		}

		// Kolom id (PRIMARY KEY = unique index) -> valid.
		okRes, err := ValidateRevision(ctx, target, RevisionSpec{
			Mode:        "batch",
			SQLUp:       fmt.Sprintf("UPDATE %s SET val = val + 1 WHERE id > {{lo}} AND id <= {{hi}};", tableName),
			SQLDown:     strPtr(fmt.Sprintf("UPDATE %s SET val = val - 1 WHERE id > {{lo}} AND id <= {{hi}};", tableName)),
			BatchTable:  strPtr(tableName),
			BatchColumn: strPtr("id"),
			BatchSize:   intPtr(500),
		})
		if err != nil || !okRes.Valid {
			t.Fatalf("expected valid batch on primary key, got err=%v res=%#v", err, okRes)
		}

		// Kolom val (tanpa unique index) -> ditolak.
		badRes, err := ValidateRevision(ctx, target, RevisionSpec{
			Mode:        "batch",
			SQLUp:       fmt.Sprintf("UPDATE %s SET val = val + 1 WHERE val > {{lo}} AND val <= {{hi}};", tableName),
			SQLDown:     strPtr(fmt.Sprintf("UPDATE %s SET val = val - 1 WHERE val > {{lo}} AND val <= {{hi}};", tableName)),
			BatchTable:  strPtr(tableName),
			BatchColumn: strPtr("val"),
			BatchSize:   intPtr(500),
		})
		if err != nil || badRes.Valid {
			t.Fatalf("expected invalid batch on non-unique column, got err=%v res=%#v", err, badRes)
		}
	})

	t.Run("view-only user cannot create, validate, or submit", func(t *testing.T) {
		denied := NewHandler(platform, testManager{pool: target}, testAuthorizer{err: authz.ErrForbidden})
		deniedMux := http.NewServeMux()
		denied.Register(deniedMux)

		rec := httptest.NewRecorder()
		deniedMux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/databases/%d/migrations", dbID), strings.NewReader(`{"title":"denied","sqlUp":"SELECT 1"}`))))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("create status = %d, want 403; body=%s", rec.Code, rec.Body.String())
		}

		// Buat fixture migration lalu coba validate & submit sebagai view-only user.
		createRec := httptest.NewRecorder()
		mux.ServeHTTP(createRec, asUser(httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/databases/%d/migrations", dbID), strings.NewReader(`{"title":"fixture","sqlUp":"SELECT 1","sqlDown":"SELECT 1"}`))))
		var created MigrationDetail
		if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
			t.Fatalf("decode fixture: %v", err)
		}

		for _, suffix := range []string{"validate", "submit"} {
			r := httptest.NewRecorder()
			deniedMux.ServeHTTP(r, asUser(httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/migrations/%d/%s", created.ID, suffix), nil)))
			if r.Code != http.StatusForbidden {
				t.Fatalf("%s status = %d, want 403; body=%s", suffix, r.Code, r.Body.String())
			}
		}
	})
}
