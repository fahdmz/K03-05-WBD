package worker

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fahdmz/K03-05-WBD/internal/metrics"
	"github.com/jackc/pgx/v5/pgxpool"
)

// test ujung-ke-ujung: job batch lewat alur worker asli (claim → lock → plan → batch → finalize)

func e2eTarget(t *testing.T, f *fixture) (int64, *pgxpool.Pool) {
	t.Helper()
	dbID, target := f.addDB()
	if _, err := target.Exec(context.Background(), `
		CREATE TABLE t (id SERIAL PRIMARY KEY, price INT NOT NULL, d INT NOT NULL DEFAULT 1);
		INSERT INTO t (price) SELECT 100 FROM generate_series(1, 1000);`); err != nil {
		t.Fatalf("siapin target: %v", err)
	}
	return dbID, target
}

var e2eRev = rev{
	up:    "UPDATE t SET price = price + 1 / d WHERE id > {{lo}} AND id <= {{hi}}",
	down:  "UPDATE t SET price = price - 1 WHERE id > {{lo}} AND id <= {{hi}}",
	mode:  "batch",
	table: "t",
	col:   "id",
	size:  100,
}

func priceSum(t *testing.T, target *pgxpool.Pool) int {
	t.Helper()
	return count(t, target, `SELECT sum(price)::int FROM t`)
}

func TestE2EBatchApplyRollback(t *testing.T) {
	f := newFixture(t)
	dbID, target := e2eTarget(t, f)
	w := f.newWorker()
	mID, rID := f.newMigration(dbID, "naikin harga", e2eRev)

	jobID, execID := f.enqueue(dbID, mID, rID, "apply", nil)
	drain(t, w)
	s := f.job(jobID)
	wantStatus(t, s, "success", "success")
	if s.Batches != 10 || s.Rows != 1000 {
		t.Fatalf("progress %d batch %d row", s.Batches, s.Rows)
	}
	if got := priceSum(t, target); got != 101000 {
		t.Fatalf("sum setelah apply = %d", got)
	}
	if n := count(t, f.platform, `SELECT count(*) FROM migration_history WHERE execution_id = $1 AND action = 'apply' AND batches = 10`, execID); n != 1 {
		t.Fatalf("history apply = %d", n)
	}

	rbID, rbExec := f.enqueue(dbID, mID, rID, "rollback", &execID)
	drain(t, w)
	wantStatus(t, f.job(rbID), "success", "success")
	if got := priceSum(t, target); got != 100000 {
		t.Fatalf("sum setelah rollback = %d", got)
	}
	if n := count(t, f.platform, `SELECT count(*) FROM migration_history WHERE execution_id = $1 AND reverts_execution_id = $2`, rbExec, execID); n != 1 {
		t.Fatalf("history rollback = %d", n)
	}
	if n := count(t, f.platform, `SELECT count(*) FROM migrations WHERE id = $1 AND NOT is_applied`, mID); n != 1 {
		t.Fatal("migration harusnya nggak aktif setelah rollback")
	}
	// down jalan urut terbalik
	var order string
	if err := target.QueryRow(context.Background(), `
		SELECT string_agg(batch_no::text, ',' ORDER BY committed_at, batch_no DESC)
		FROM _platform.batch_checkpoints WHERE execution_id = $1 AND direction = 'down'`, rbExec,
	).Scan(&order); err != nil {
		t.Fatal(err)
	}
	if order != "10,9,8,7,6,5,4,3,2,1" {
		t.Fatalf("urutan down = %s", order)
	}
}

func TestE2EBatchPartialResume(t *testing.T) {
	f := newFixture(t)
	dbID, target := e2eTarget(t, f)
	w := f.newWorker()
	mID, rID := f.newMigration(dbID, "naikin harga", e2eRev)
	if _, err := target.Exec(context.Background(), `UPDATE t SET d = 0 WHERE id = 550`); err != nil {
		t.Fatal(err)
	}

	jobID, execID := f.enqueue(dbID, mID, rID, "apply", nil)
	drain(t, w)
	s := f.job(jobID)
	wantStatus(t, s, "partial", "partial")
	if s.Batches != 5 || s.SQLState == nil || *s.SQLState != "22012" {
		t.Fatalf("partial: batch=%d sqlstate=%v", s.Batches, s.SQLState)
	}
	if got := priceSum(t, target); got != 100500 {
		t.Fatalf("batch 1-5 harusnya tetap committed, sum = %d", got)
	}

	// job lain di DB yang sama ketahan selama masih partial
	m2, r2 := f.newMigration(dbID, "lain", rev{up: "SELECT 1", down: "SELECT 1"})
	otherID, _ := f.enqueue(dbID, m2, r2, "apply", nil)
	runOnce(t, w)
	runOnce(t, w)
	if st := f.job(otherID); st.Status != "queued" || st.Attempt != 0 {
		t.Fatalf("job lain harusnya ketahan, status=%s attempt=%d", st.Status, st.Attempt)
	}

	if _, err := target.Exec(context.Background(), `UPDATE t SET d = 1`); err != nil {
		t.Fatal(err)
	}
	resID, _ := f.enqueue(dbID, mID, rID, "resume", &execID)
	drain(t, w)
	wantStatus(t, f.job(resID), "success", "success")
	wantStatus(t, f.job(jobID), "success", "success")
	wantStatus(t, f.job(otherID), "success", "success")
	if got := priceSum(t, target); got != 101000 {
		t.Fatalf("sum setelah resume = %d", got)
	}
	// attempt partial tercatat di job asal, sukses-nya di job resume (dicatat sebagai apply)
	if n := count(t, f.platform, `
		SELECT count(*) FROM migration_history WHERE execution_id = $1 AND status = 'partial' AND batches = 5`, execID); n != 1 {
		t.Fatalf("history partial = %d", n)
	}
	if n := count(t, f.platform, `
		SELECT count(*) FROM migration_history h JOIN jobs j ON j.execution_id = h.execution_id
		WHERE j.id = $1 AND h.action = 'apply' AND h.status = 'success' AND h.batches = 10`, resID); n != 1 {
		t.Fatalf("history resume = %d", n)
	}
	if n := count(t, f.platform, `SELECT count(*) FROM migrations WHERE id = $1 AND is_applied AND version = 1`, mID); n != 1 {
		t.Fatal("migration harusnya aktif setelah resume")
	}
}

func TestE2EBatchRevertPartial(t *testing.T) {
	f := newFixture(t)
	dbID, target := e2eTarget(t, f)
	w := f.newWorker()
	mID, rID := f.newMigration(dbID, "naikin harga", e2eRev)
	if _, err := target.Exec(context.Background(), `UPDATE t SET d = 0 WHERE id = 350`); err != nil {
		t.Fatal(err)
	}

	jobID, execID := f.enqueue(dbID, mID, rID, "apply", nil)
	drain(t, w)
	wantStatus(t, f.job(jobID), "partial", "partial")

	revID, _ := f.enqueue(dbID, mID, rID, "revert", &execID)
	drain(t, w)
	wantStatus(t, f.job(revID), "success", "success")
	orig := f.job(jobID)
	wantStatus(t, orig, "failed", "failed")
	if orig.LastErr == nil || !strings.Contains(*orig.LastErr, "revert") {
		t.Fatalf("last_error job asal = %v", orig.LastErr)
	}
	if got := priceSum(t, target); got != 100000 {
		t.Fatalf("sum setelah revert = %d", got)
	}
	// apply partial + revert sukses tercatat, posisi migration nggak berubah (belum pernah aktif)
	if n := count(t, f.platform, `SELECT count(*) FROM migration_history WHERE execution_id = $1 AND status = 'partial'`, execID); n != 1 {
		t.Fatalf("history apply partial = %d", n)
	}
	if n := count(t, f.platform, `
		SELECT count(*) FROM migration_history h JOIN jobs j ON j.execution_id = h.execution_id
		WHERE j.id = $1 AND h.action = 'revert' AND h.status = 'success'`, revID); n != 1 {
		t.Fatalf("history revert = %d", n)
	}
	if n := count(t, f.platform, `SELECT count(*) FROM migrations WHERE id = $1 AND NOT is_applied AND version IS NULL`, mID); n != 1 {
		t.Fatal("migration nggak boleh aktif setelah revert")
	}
}

func TestE2EMetrics(t *testing.T) {
	f := newFixture(t)
	dbID, _ := e2eTarget(t, f)
	w := f.newWorker()
	reg := metrics.New()
	w.SetMetrics(NewMetrics(reg, f.platform))
	mID, rID := f.newMigration(dbID, "naikin harga", e2eRev)
	f.enqueue(dbID, mID, rID, "apply", nil)
	drain(t, w)

	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	out := rec.Body.String()
	for _, want := range []string{
		`worker_jobs_finished_total{status="success",action="apply",mode="batch"} 1`,
		`worker_batches_committed_total{direction="up"} 10`,
		`worker_rows_committed_total{direction="up"} 1000`,
		`worker_jobs{status="success"} 1`,
		`worker_jobs{status="queued"} 0`,
		`worker_lock_wait_seconds_count{result="acquired"} 1`,
		`worker_queue_wait_seconds_count 1`,
		`worker_migration_duration_seconds_count{status="success",action="apply",mode="batch"} 1`,
		`worker_db_pool_connections{state="max"}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics nggak ada %q\n%s", want, out)
		}
	}
}

// rollback dari apply batch yang selesai lewat resume: history sukses ada di job resume,
// tapi checkpoint up-nya di job asal (root) → rollback harus nemu semua 10 batch
func TestE2EBatchRollbackAfterResume(t *testing.T) {
	f := newFixture(t)
	dbID, target := e2eTarget(t, f)
	w := f.newWorker()
	mID, rID := f.newMigration(dbID, "naikin harga", e2eRev)
	if _, err := target.Exec(context.Background(), `UPDATE t SET d = 0 WHERE id = 450`); err != nil {
		t.Fatal(err)
	}
	jobID, execID := f.enqueue(dbID, mID, rID, "apply", nil)
	drain(t, w)
	wantStatus(t, f.job(jobID), "partial", "partial")
	if _, err := target.Exec(context.Background(), `UPDATE t SET d = 1`); err != nil {
		t.Fatal(err)
	}
	resID, resExec := f.enqueue(dbID, mID, rID, "resume", &execID)
	drain(t, w)
	wantStatus(t, f.job(resID), "success", "success")

	rbID, rbExec := f.enqueue(dbID, mID, rID, "rollback", &resExec)
	drain(t, w)
	wantStatus(t, f.job(rbID), "success", "success")
	if got := priceSum(t, target); got != 100000 {
		t.Fatalf("sum setelah rollback = %d", got)
	}
	if n := count(t, target, `SELECT count(*) FROM _platform.batch_checkpoints WHERE execution_id = $1 AND direction = 'down'`, rbExec); n != 10 {
		t.Fatalf("batch down = %d", n)
	}
	if n := count(t, f.platform, `
		SELECT count(*) FROM migration_history WHERE execution_id = $1 AND action = 'rollback'
		  AND status = 'success' AND reverts_execution_id = $2`, rbExec, resExec); n != 1 {
		t.Fatalf("history rollback = %d", n)
	}
	if n := count(t, f.platform, `SELECT count(*) FROM migrations WHERE id = $1 AND NOT is_applied`, mID); n != 1 {
		t.Fatal("migration harusnya nggak aktif")
	}
}
