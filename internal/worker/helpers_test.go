package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"
	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testDatabaseURL() string {
	if u := os.Getenv("TEST_DATABASE_URL"); u != "" {
		return u
	}
	return "postgres://test:test@localhost:5434/test?sslmode=disable"
}

func randName(prefix string) string {
	b := make([]byte, 5)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func adminPool(t *testing.T) *pgxpool.Pool {
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
	return admin
}

// setupPlatform: schema acak berisi 001 + 002
func setupPlatform(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	admin := adminPool(t)
	schema := randName("wk_")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("buat schema: %v", err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		admin.Close()
	})

	cfg, err := pgxpool.ParseConfig(testDatabaseURL())
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 20
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("koneksi schema test: %v", err)
	}
	t.Cleanup(db.Close)
	for _, f := range []string{"../../migrations/001_init.sql", "../../migrations/002_m2.sql"} {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("baca %s: %v", f, err)
		}
		if _, err := db.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("terapkan %s: %v", f, err)
		}
	}
	return db
}

// setupTarget: database target acak (CREATE DATABASE), di-drop pas selesai
func setupTarget(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	admin := adminPool(t)
	name := randName("wkt_")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("buat database target: %v", err)
	}
	cfg, err := pgxpool.ParseConfig(testDatabaseURL())
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.Database = name
	cfg.MaxConns = 10
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("koneksi target: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close()
	})
	return db
}

// stubManager: Pool balikin pool target yang udah disiapin test
type stubManager struct {
	pools map[int64]*pgxpool.Pool
	err   error
}

func (m *stubManager) Pool(ctx context.Context, dbID int64) (*pgxpool.Pool, error) {
	if m.err != nil {
		return nil, m.err
	}
	if p := m.pools[dbID]; p != nil {
		return p, nil
	}
	return nil, dbmanager.ErrDatabaseNotFound
}

func (m *stubManager) Test(context.Context, dbmanager.ConnConfig) error { return nil }
func (m *stubManager) Invalidate(int64)                                 {}

type fixture struct {
	t        *testing.T
	platform *pgxpool.Pool
	userID   int64
	mgr      *stubManager
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, platform: setupPlatform(t), mgr: &stubManager{pools: map[int64]*pgxpool.Pool{}}}
	if err := f.platform.QueryRow(context.Background(), `
		INSERT INTO users (username, password_hash) VALUES ('tester', 'x') RETURNING id`,
	).Scan(&f.userID); err != nil {
		t.Fatalf("buat user: %v", err)
	}
	return f
}

// addDB daftarin database target baru, balikin id db_connections + pool-nya
func (f *fixture) addDB() (int64, *pgxpool.Pool) {
	f.t.Helper()
	target := setupTarget(f.t)
	var id int64
	if err := f.platform.QueryRow(context.Background(), `
		INSERT INTO db_connections (name, host, dbname, username, password_encrypted)
		VALUES ($1, 'localhost', 'x', 'x', $2) RETURNING id`, randName("db_"), []byte{0},
	).Scan(&id); err != nil {
		f.t.Fatalf("buat koneksi: %v", err)
	}
	// group dengan leader + akses Edit, biar lolos workflow.Revalidate yang asli
	ctx := context.Background()
	var gID int64
	if err := f.platform.QueryRow(ctx, `
		INSERT INTO groups (name, leader_user_id) VALUES ($1, $2) RETURNING id`, randName("g_"), f.userID,
	).Scan(&gID); err != nil {
		f.t.Fatalf("buat group: %v", err)
	}
	if _, err := f.platform.Exec(ctx, `
		INSERT INTO group_members (group_id, user_id) VALUES ($1, $2);`, gID, f.userID); err != nil {
		f.t.Fatalf("anggota group: %v", err)
	}
	if _, err := f.platform.Exec(ctx, `
		INSERT INTO group_db_access (group_id, db_connection_id, level) VALUES ($1, $2, 'edit')`, gID, id); err != nil {
		f.t.Fatalf("akses group: %v", err)
	}
	f.mgr.pools[id] = target
	return id, target
}

type rev struct {
	up, down     string
	irreversible bool
	mode         string // "" = normal
	table, col   string
	size         int
}

// newMigration bikin migration + revisi submitted, balikin (migrationID, revisionID)
func (f *fixture) newMigration(dbID int64, title string, r rev) (int64, int64) {
	f.t.Helper()
	ctx := context.Background()
	var mID, rID int64
	if err := f.platform.QueryRow(ctx, `
		INSERT INTO migrations (db_connection_id, title, created_by) VALUES ($1, $2, $3) RETURNING id`,
		dbID, title, f.userID,
	).Scan(&mID); err != nil {
		f.t.Fatalf("buat migration: %v", err)
	}
	mode := r.mode
	if mode == "" {
		mode = "normal"
	}
	var down, reason, table, col *string
	var size *int
	if r.irreversible {
		s := "test irreversible"
		reason = &s
	} else {
		down = &r.down
	}
	if mode == "batch" {
		table, col, size = &r.table, &r.col, &r.size
	}
	if err := f.platform.QueryRow(ctx, `
		INSERT INTO migration_revisions (
			migration_id, revision_no, status, sql_up, sql_down, irreversible, irreversible_reason,
			mode, batch_table, batch_column, batch_size, created_by, submitted_at
		) VALUES ($1, 1, 'submitted', $2, $3, $4, $5, $6, $7, $8, $9, $10, now()) RETURNING id`,
		mID, r.up, down, r.irreversible, reason, mode, table, col, size, f.userID,
	).Scan(&rID); err != nil {
		f.t.Fatalf("buat revisi: %v", err)
	}
	if _, err := f.platform.Exec(ctx, `UPDATE migrations SET current_revision_id = $2 WHERE id = $1`, mID, rID); err != nil {
		f.t.Fatalf("sambung revisi: %v", err)
	}
	return mID, rID
}

// enqueue bikin change request approved + job queued (lewat queue.Enqueue punya tim).
// target = target_execution_id (buat rollback/resume/revert), boleh nil.
func (f *fixture) enqueue(dbID, mID, rID int64, action string, target *string) (jobID int64, execID string) {
	f.t.Helper()
	ctx := context.Background()
	tx, err := f.platform.Begin(ctx)
	if err != nil {
		f.t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	var crID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO change_requests (db_connection_id, migration_id, revision_id, action,
			target_execution_id, status, requested_by)
		VALUES ($1, $2, $3, $4, $5, 'approved', $6) RETURNING id`,
		dbID, mID, rID, action, target, f.userID,
	).Scan(&crID); err != nil {
		f.t.Fatalf("buat change request: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO jobs (change_request_id, db_connection_id, execution_id, status, trace_context)
		VALUES ($1, $2, gen_random_uuid(), 'queued', '{}') RETURNING id, execution_id::text`,
		crID, dbID,
	).Scan(&jobID, &execID); err != nil {
		f.t.Fatalf("buat job: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE change_requests SET status = 'queued' WHERE id = $1`, crID); err != nil {
		f.t.Fatalf("update cr: %v", err)
	}
	// approval dari leader tiap group yang punya akses
	if _, err := tx.Exec(ctx, `
		INSERT INTO approvals (change_request_id, group_id, approver_id, decision)
		SELECT $1, g.id, g.leader_user_id, 'approve'
		FROM group_db_access a JOIN groups g ON g.id = a.group_id
		WHERE a.db_connection_id = $2`, crID, dbID); err != nil {
		f.t.Fatalf("approval: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		f.t.Fatalf("commit: %v", err)
	}
	return jobID, execID
}

type jobState struct {
	Status   string
	CRStatus string
	Attempt  int
	Batches  int
	Rows     int64
	LastErr  *string
	SQLState *string
}

func (f *fixture) job(id int64) jobState {
	f.t.Helper()
	var s jobState
	if err := f.platform.QueryRow(context.Background(), `
		SELECT j.status, cr.status, j.attempt, j.batches_committed, j.rows_committed, j.last_error, j.last_sqlstate
		FROM jobs j JOIN change_requests cr ON cr.id = j.change_request_id WHERE j.id = $1`, id,
	).Scan(&s.Status, &s.CRStatus, &s.Attempt, &s.Batches, &s.Rows, &s.LastErr, &s.SQLState); err != nil {
		f.t.Fatalf("baca job %d: %v", id, err)
	}
	return s
}

// newWorker: worker buat test, tanpa jeda backoff / tunggu
func (f *fixture) newWorker() *Worker {
	w := New(f.platform, f.mgr)
	w.backoff = func(int) time.Duration { return 0 }
	w.waitDelay = 0
	w.pollInterval = 20 * time.Millisecond
	return w
}

func runOnce(t *testing.T, w *Worker) bool {
	t.Helper()
	got, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	return got
}

// drain: RunOnce terus sampe antrean kosong
func drain(t *testing.T, w *Worker) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		if !runOnce(t, w) {
			return i
		}
	}
	t.Fatalf("antrean nggak abis-abis")
	return 0
}

func count(t *testing.T, q store.Querier, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := q.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

func hasTable(t *testing.T, q store.Querier, name string) bool {
	t.Helper()
	var ok bool
	if err := q.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok); err != nil {
		t.Fatalf("cek tabel %s: %v", name, err)
	}
	return ok
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.platform.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("exec %q: %v", sql, err)
	}
}

func wantStatus(t *testing.T, s jobState, job, cr string) {
	t.Helper()
	if s.Status != job || s.CRStatus != cr {
		le := ""
		if s.LastErr != nil {
			le = *s.LastErr
		}
		t.Fatalf("status job=%s cr=%s, mau job=%s cr=%s (last_error=%q)", s.Status, s.CRStatus, job, cr, le)
	}
}
