package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/authz"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testSchema = "history_test"

type denyAll struct{}

func (denyAll) EffectiveLevel(context.Context, int64, int64) (authz.Level, error) {
	return authz.None, nil
}

func (denyAll) Require(context.Context, int64, int64, authz.Level) error {
	return authz.ErrForbidden
}

func testDatabaseURL() string {
	if u := os.Getenv("TEST_DATABASE_URL"); u != "" {
		return u
	}
	return "postgres://test:test@localhost:5434/test?sslmode=disable"
}

func setupDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, testDatabaseURL())
	if err != nil {
		t.Fatalf("koneksi test-db: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("test-db tidak bisa dihubungi, jalankan docker compose up -d test-db: %v", err)
	}
	t.Cleanup(func() {
		defer admin.Close()
		admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+testSchema+" CASCADE")
	})
	if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+testSchema+" CASCADE; CREATE SCHEMA "+testSchema); err != nil {
		t.Fatalf("reset schema: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(testDatabaseURL())
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = testSchema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("koneksi schema test: %v", err)
	}
	t.Cleanup(db.Close)

	for _, file := range []string{"../../migrations/001_init.sql", "../../migrations/002_m2.sql"} {
		schema, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("baca %s: %v", file, err)
		}
		if _, err := db.Exec(ctx, string(schema)); err != nil {
			t.Fatalf("terapkan %s: %v", file, err)
		}
	}
	return db
}

type fixture struct {
	t      *testing.T
	db     *pgxpool.Pool
	userID int64
	dbID   int64
}

func newFixture(t *testing.T) *fixture {
	db := setupDB(t)
	f := &fixture{t: t, db: db}
	ctx := context.Background()
	if err := db.QueryRow(ctx, `
		INSERT INTO users (username, password_hash) VALUES ('tester', 'x') RETURNING id`,
	).Scan(&f.userID); err != nil {
		t.Fatalf("buat user: %v", err)
	}
	if err := db.QueryRow(ctx, `
		INSERT INTO db_connections (name, host, dbname, username, password_encrypted)
		VALUES ('shop', 'localhost', 'shop', 'target', $1) RETURNING id`, []byte{0},
	).Scan(&f.dbID); err != nil {
		t.Fatalf("buat koneksi database: %v", err)
	}
	return f
}

func (f *fixture) newMigration(title string, irreversible bool) (int64, int64) {
	f.t.Helper()
	ctx := context.Background()
	var migrationID, revisionID int64
	if err := f.db.QueryRow(ctx, `
		INSERT INTO migrations (db_connection_id, title, created_by) VALUES ($1, $2, $3) RETURNING id`,
		f.dbID, title, f.userID,
	).Scan(&migrationID); err != nil {
		f.t.Fatalf("buat migration %s: %v", title, err)
	}

	var sqlDown, reason *string
	if irreversible {
		r := "menghapus data"
		reason = &r
	} else {
		d := "SELECT 'down " + title + "'"
		sqlDown = &d
	}
	if err := f.db.QueryRow(ctx, `
		INSERT INTO migration_revisions (
			migration_id, revision_no, status, sql_up, sql_down, irreversible, irreversible_reason,
			created_by, submitted_at
		) VALUES ($1, 1, 'submitted', $2, $3, $4, $5, $6, now()) RETURNING id`,
		migrationID, "SELECT 'up "+title+"'", sqlDown, irreversible, reason, f.userID,
	).Scan(&revisionID); err != nil {
		f.t.Fatalf("buat revisi %s: %v", title, err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE migrations SET current_revision_id = $2 WHERE id = $1`, migrationID, revisionID); err != nil {
		f.t.Fatalf("sambungkan revisi %s: %v", title, err)
	}
	return migrationID, revisionID
}

func (f *fixture) record(action Action, migrationID, revisionID int64, reverts *string) string {
	f.t.Helper()
	ctx := context.Background()
	var executionID string
	if err := f.db.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&executionID); err != nil {
		f.t.Fatalf("buat execution id: %v", err)
	}

	tx, err := f.db.Begin(ctx)
	if err != nil {
		f.t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	now := time.Now()
	err = Record(ctx, tx, Result{
		Plan: Plan{
			DatabaseID:         f.dbID,
			MigrationID:        migrationID,
			RevisionID:         revisionID,
			RequestedBy:        f.userID,
			Action:             action,
			Mode:               "normal",
			RevertsExecutionID: reverts,
		},
		ExecutionID: executionID,
		ExecutedSQL: "SELECT 1",
		StartedAt:   now,
		CommittedAt: now,
	})
	if err != nil {
		f.t.Fatalf("record %s: %v", action, err)
	}
	if err := tx.Commit(ctx); err != nil {
		f.t.Fatalf("commit: %v", err)
	}
	return executionID
}

func (f *fixture) call(authorizer authz.Authorizer, method, path string) *httptest.ResponseRecorder {
	f.t.Helper()
	mux := http.NewServeMux()
	NewHandler(f.db, authorizer).Register(mux)
	req := httptest.NewRequest(method, path, nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: f.userID, Username: "tester"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) history(hide bool) []string {
	f.t.Helper()
	rec := f.call(authz.AllowAll{}, http.MethodGet, fmt.Sprintf("/api/databases/%d/history?hideRollback=%t", f.dbID, hide))
	if rec.Code != http.StatusOK {
		f.t.Fatalf("history status = %d: %s", rec.Code, rec.Body.String())
	}
	var entries []Entry
	if err := json.NewDecoder(rec.Body).Decode(&entries); err != nil {
		f.t.Fatalf("decode history: %v", err)
	}
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.ExecutionID
	}
	return ids
}

func assertIDs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d entries %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestHideRollbackExample(t *testing.T) {
	e2 := "E2"
	entries := []Entry{
		{ExecutionID: "E1", Action: "apply"},
		{ExecutionID: "E2", Action: "apply"},
		{ExecutionID: "E3", Action: "rollback", RevertsExecutionID: &e2},
		{ExecutionID: "E4", Action: "forward"},
		{ExecutionID: "E5", Action: "apply"},
	}
	got := HideRollback(entries)
	ids := make([]string, len(got))
	for i, e := range got {
		ids[i] = e.ExecutionID
	}
	assertIDs(t, ids, []string{"E1", "E4", "E5"})
}

func TestRollbackForwardFlow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	a, ra := f.newMigration("A", false)
	b, rb := f.newMigration("B", false)
	c, rc := f.newMigration("C", false)

	e1 := f.record(Apply, a, ra, nil)
	e2 := f.record(Apply, b, rb, nil)

	if _, err := CheckRollback(ctx, f.db, a); !errors.Is(err, ErrNotTop) {
		t.Fatalf("rollback A saat B aktif: err = %v, want ErrNotTop", err)
	}
	target, err := CheckRollback(ctx, f.db, b)
	if err != nil {
		t.Fatalf("rollback B: %v", err)
	}
	if target.ExecutionID != e2 {
		t.Fatalf("target rollback = %s, want %s", target.ExecutionID, e2)
	}
	e3 := f.record(Rollback, b, target.RevisionID, &target.ExecutionID)

	if _, err := CheckForward(ctx, f.db, c); !errors.Is(err, ErrNeverApplied) {
		t.Fatalf("forward C yang belum pernah jalan: err = %v, want ErrNeverApplied", err)
	}
	next, err := CheckForward(ctx, f.db, b)
	if err != nil {
		t.Fatalf("forward B: %v", err)
	}
	e4 := f.record(Forward, b, next.RevisionID, nil)
	e5 := f.record(Apply, c, rc, nil)

	assertIDs(t, f.history(false), []string{e1, e2, e3, e4, e5})
	assertIDs(t, f.history(true), []string{e1, e4, e5})

	var versionB, versionC int
	f.db.QueryRow(ctx, `SELECT version FROM migrations WHERE id = $1`, b).Scan(&versionB)
	f.db.QueryRow(ctx, `SELECT version FROM migrations WHERE id = $1`, c).Scan(&versionC)
	if versionB != 2 || versionC != 3 {
		t.Fatalf("versi B = %d, C = %d, want 2 dan 3", versionB, versionC)
	}
}

func TestRules(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	a, ra := f.newMigration("A", false)
	f.record(Apply, a, ra, nil)
	if _, err := CheckForward(ctx, f.db, a); !errors.Is(err, ErrAlreadyApplied) {
		t.Fatalf("forward migration aktif: err = %v, want ErrAlreadyApplied", err)
	}

	pending, _ := f.newMigration("belum jalan", false)
	if _, err := CheckRollback(ctx, f.db, pending); !errors.Is(err, ErrNotApplied) {
		t.Fatalf("rollback migration belum jalan: err = %v, want ErrNotApplied", err)
	}

	b, rb := f.newMigration("B", false)
	f.record(Apply, b, rb, nil)
	target, err := CheckRollback(ctx, f.db, b)
	if err != nil {
		t.Fatalf("rollback B: %v", err)
	}
	f.record(Rollback, b, target.RevisionID, &target.ExecutionID)
	d, rd := f.newMigration("D", false)
	f.record(Apply, d, rd, nil)
	if _, err := CheckForward(ctx, f.db, b); !errors.Is(err, ErrNotNextForward) {
		t.Fatalf("forward B setelah D diterapkan: err = %v, want ErrNotNextForward", err)
	}

	irr, rirr := f.newMigration("irreversible", true)
	f.record(Apply, irr, rirr, nil)
	if _, err := CheckRollback(ctx, f.db, irr); !errors.Is(err, ErrIrreversible) {
		t.Fatalf("rollback irreversible: err = %v, want ErrIrreversible", err)
	}

	if _, err := CheckRollback(ctx, f.db, 999999); !errors.Is(err, ErrMigrationNotFound) {
		t.Fatalf("rollback migration fiktif: err = %v, want ErrMigrationNotFound", err)
	}
}

func TestHistoryCannotBeUpdated(t *testing.T) {
	f := newFixture(t)
	a, ra := f.newMigration("A", false)
	f.record(Apply, a, ra, nil)
	if _, err := f.db.Exec(context.Background(), `UPDATE migration_history SET mode = 'batch'`); err == nil {
		t.Fatal("update migration_history seharusnya ditolak")
	}
}

func TestRollbackEndpoint(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, ra := f.newMigration("A", false)
	e1 := f.record(Apply, a, ra, nil)
	path := fmt.Sprintf("/api/migrations/%d/rollback", a)

	if rec := f.call(denyAll{}, http.MethodPost, path); rec.Code != http.StatusForbidden {
		t.Fatalf("tanpa akses: status = %d, want 403", rec.Code)
	}

	rec := f.call(authz.AllowAll{}, http.MethodPost, path)
	if rec.Code != http.StatusCreated {
		t.Fatalf("rollback: status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var action, status string
	var target *string
	if err := f.db.QueryRow(ctx, `
		SELECT action, status, target_execution_id::text FROM change_requests WHERE migration_id = $1`, a,
	).Scan(&action, &status, &target); err != nil {
		t.Fatalf("baca change request: %v", err)
	}
	if action != "rollback" || status != "pending_review" || target == nil || *target != e1 {
		t.Fatalf("change request = %s/%s/%v, want rollback/pending_review/%s", action, status, target, e1)
	}

	if rec := f.call(authz.AllowAll{}, http.MethodPost, path); rec.Code != http.StatusConflict {
		t.Fatalf("rollback kedua: status = %d, want 409", rec.Code)
	}

	b, _ := f.newMigration("B", false)
	if rec := f.call(authz.AllowAll{}, http.MethodPost, fmt.Sprintf("/api/migrations/%d/forward", b)); rec.Code != http.StatusConflict {
		t.Fatalf("forward migration yang belum pernah jalan: status = %d, want 409", rec.Code)
	}

	mux := http.NewServeMux()
	NewHandler(f.db, authz.AllowAll{}).Register(mux)
	noUser := httptest.NewRecorder()
	mux.ServeHTTP(noUser, httptest.NewRequest(http.MethodPost, path, nil))
	if noUser.Code != http.StatusUnauthorized {
		t.Fatalf("tanpa login: status = %d, want 401", noUser.Code)
	}
}

func TestJobEndpoints(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, ra := f.newMigration("A", false)

	var requestID, jobID int64
	if err := f.db.QueryRow(ctx, `
		INSERT INTO change_requests (db_connection_id, migration_id, revision_id, action, status, requested_by)
		VALUES ($1, $2, $3, 'apply', 'queued', $4) RETURNING id`, f.dbID, a, ra, f.userID,
	).Scan(&requestID); err != nil {
		t.Fatalf("buat change request: %v", err)
	}
	if err := f.db.QueryRow(ctx, `
		INSERT INTO jobs (change_request_id, db_connection_id, execution_id, status)
		VALUES ($1, $2, gen_random_uuid(), 'queued') RETURNING id`, requestID, f.dbID,
	).Scan(&jobID); err != nil {
		t.Fatalf("buat job: %v", err)
	}

	rec := f.call(authz.AllowAll{}, http.MethodGet, fmt.Sprintf("/api/jobs/%d", jobID))
	if rec.Code != http.StatusOK {
		t.Fatalf("get job: status = %d: %s", rec.Code, rec.Body.String())
	}
	var job Job
	if err := json.NewDecoder(rec.Body).Decode(&job); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if job.Status != "queued" || job.MigrationTitle != "A" || job.Action != "apply" {
		t.Fatalf("job = %+v", job)
	}

	if rec := f.call(denyAll{}, http.MethodGet, fmt.Sprintf("/api/jobs/%d", jobID)); rec.Code != http.StatusForbidden {
		t.Fatalf("get job tanpa akses: status = %d, want 403", rec.Code)
	}
	if rec := f.call(authz.AllowAll{}, http.MethodGet, "/api/jobs/999999"); rec.Code != http.StatusNotFound {
		t.Fatalf("job fiktif: status = %d, want 404", rec.Code)
	}

	rec = f.call(authz.AllowAll{}, http.MethodGet, fmt.Sprintf("/api/databases/%d/jobs", f.dbID))
	var jobs []Job
	if err := json.NewDecoder(rec.Body).Decode(&jobs); err != nil || len(jobs) != 1 {
		t.Fatalf("list jobs = %d item, err %v", len(jobs), err)
	}

	if rec := f.call(authz.AllowAll{}, http.MethodPost, fmt.Sprintf("/api/jobs/%d/resume", jobID)); rec.Code != http.StatusConflict {
		t.Fatalf("resume job non-batch: status = %d, want 409", rec.Code)
	}
}
