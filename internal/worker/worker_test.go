package worker

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/history"
	"github.com/fahdmz/K03-05-WBD/internal/queue"
	"github.com/jackc/pgx/v5"
)

func TestApplyNormal(t *testing.T) {
	f := newFixture(t)
	db, target := f.addDB()
	mID, rID := f.newMigration(db, "buat tabel", rev{up: "CREATE TABLE items (id INT); INSERT INTO items VALUES (1), (2)", down: "DROP TABLE items"})
	jobID, exec := f.enqueue(db, mID, rID, "apply", nil)
	w := f.newWorker()

	if !runOnce(t, w) {
		t.Fatal("job nggak ke-claim")
	}
	wantStatus(t, f.job(jobID), "success", "success")
	if s := f.job(jobID); s.Attempt != 1 || s.LastErr != nil {
		t.Fatalf("attempt=%d last_error=%v", s.Attempt, s.LastErr)
	}
	if n := count(t, target, `SELECT count(*) FROM items`); n != 2 {
		t.Fatalf("isi items = %d", n)
	}
	if n := count(t, target, `SELECT count(*) FROM _platform.executions WHERE execution_id = $1 AND direction = 'up'`, exec); n != 1 {
		t.Fatalf("bukti eksekusi = %d", n)
	}
	if n := count(t, f.platform, `SELECT count(*) FROM migration_history WHERE execution_id = $1 AND action = 'apply'`, exec); n != 1 {
		t.Fatalf("history = %d", n)
	}
	var ver int
	var applied bool
	if err := f.platform.QueryRow(context.Background(), `SELECT version, is_applied FROM migrations WHERE id = $1`, mID).Scan(&ver, &applied); err != nil {
		t.Fatal(err)
	}
	if ver != 1 || !applied {
		t.Fatalf("version=%d is_applied=%v", ver, applied)
	}
	if n := count(t, f.platform, `SELECT count(*) FROM database_locks`); n != 0 {
		t.Fatalf("lock nggak dilepas")
	}
	if runOnce(t, w) {
		t.Fatal("antrean harusnya kosong")
	}
}

func TestPermanentError(t *testing.T) {
	f := newFixture(t)
	db, target := f.addDB()
	mID, rID := f.newMigration(db, "salah", rev{up: "CREATE TABLE half (id INT); SELECT * FROM tabel_gaib", down: "DROP TABLE half"})
	jobID, _ := f.enqueue(db, mID, rID, "apply", nil)
	runOnce(t, f.newWorker())

	s := f.job(jobID)
	wantStatus(t, s, "failed", "failed")
	if s.SQLState == nil || *s.SQLState != "42P01" || s.LastErr == nil {
		t.Fatalf("sqlstate=%v last_error=%v", s.SQLState, s.LastErr)
	}
	if s.Attempt != 1 {
		t.Fatalf("attempt = %d", s.Attempt)
	}
	if hasTable(t, target, "half") {
		t.Fatal("tabel half harusnya ikut rollback")
	}
	// spek rev 4: attempt gagal tetep dicatat di history
	if n := count(t, f.platform, `
		SELECT count(*) FROM migration_history
		WHERE status = 'failed' AND attempt = 1 AND sqlstate = '42P01' AND error_message IS NOT NULL AND version IS NULL`); n != 1 {
		t.Fatalf("history attempt gagal = %d", n)
	}
	if n := count(t, f.platform, `SELECT count(*) FROM migrations WHERE id = $1 AND NOT is_applied AND version IS NULL`, mID); n != 1 {
		t.Fatal("migration gagal nggak boleh dapet versi")
	}
}

func TestTransientRetryLimit(t *testing.T) {
	f := newFixture(t)
	db, _ := f.addDB()
	mID, rID := f.newMigration(db, "serialisasi", rev{up: `DO $$ BEGIN RAISE EXCEPTION 'x' USING ERRCODE = '40001'; END $$`, down: "SELECT 1"})
	jobID, _ := f.enqueue(db, mID, rID, "apply", nil)
	w := f.newWorker()

	runOnce(t, w)
	s := f.job(jobID)
	wantStatus(t, s, "queued", "queued")
	if s.Attempt != 1 || s.SQLState == nil || *s.SQLState != "40001" {
		t.Fatalf("attempt=%d sqlstate=%v", s.Attempt, s.SQLState)
	}
	for i := 2; i <= 5; i++ {
		if !runOnce(t, w) {
			t.Fatalf("attempt %d nggak ke-claim", i)
		}
		if s := f.job(jobID); s.Attempt != i {
			t.Fatalf("attempt = %d, mau %d", s.Attempt, i)
		}
	}
	s = f.job(jobID)
	wantStatus(t, s, "failed", "failed")
	if !strings.Contains(*s.LastErr, "5 percobaan") {
		t.Fatalf("last_error = %q", *s.LastErr)
	}
	if runOnce(t, w) {
		t.Fatal("job gagal masih di-claim")
	}
	if n := count(t, f.platform, `SELECT count(*) FROM migration_history WHERE status = 'failed' AND sqlstate = '40001'`); n != 5 {
		t.Fatalf("history attempt retry = %d, harusnya 5", n)
	}
}

func TestRejectTxControl(t *testing.T) {
	f := newFixture(t)
	db, target := f.addDB()
	mID, rID := f.newMigration(db, "commit", rev{up: "CREATE TABLE c1 (id INT); COMMIT; CREATE TABLE c2 (id INT)", down: "SELECT 1"})
	jobID, _ := f.enqueue(db, mID, rID, "apply", nil)
	runOnce(t, f.newWorker())

	s := f.job(jobID)
	wantStatus(t, s, "failed", "failed")
	if !strings.Contains(*s.LastErr, "perintah transaksi (COMMIT)") {
		t.Fatalf("last_error = %q", *s.LastErr)
	}
	if hasTable(t, target, "c1") || hasTable(t, target, "c2") {
		t.Fatal("SQL nggak boleh jalan")
	}
}

func TestClaimNoDuplicate(t *testing.T) {
	f := newFixture(t)
	db, _ := f.addDB()
	mID, rID := f.newMigration(db, "banyak", rev{up: "SELECT 1", down: "SELECT 1"})
	const n = 30
	for i := 0; i < n; i++ {
		f.enqueue(db, mID, rID, "apply", nil)
	}

	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for {
				c, err := queue.Claim(context.Background(), f.platform, randName("w"), time.Minute)
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				if c == nil {
					return
				}
				mu.Lock()
				seen[c.ID]++
				mu.Unlock()
			}
		}(g)
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("ke-claim %d job, mau %d", len(seen), n)
	}
	for id, k := range seen {
		if k != 1 {
			t.Fatalf("job %d di-claim %d kali", id, k)
		}
	}
	if k := count(t, f.platform, `SELECT count(*) FROM jobs WHERE status = 'running' AND worker_id IS NOT NULL AND started_at IS NOT NULL`); k != n {
		t.Fatalf("job running = %d", k)
	}
}

func TestSameDBSerialDiffDBParallel(t *testing.T) {
	f := newFixture(t)
	dbA, _ := f.addDB()
	dbB, _ := f.addDB()
	var jobs []int64
	for _, db := range []int64{dbA, dbA, dbB} {
		mID, rID := f.newMigration(db, "tidur", rev{up: "SELECT pg_sleep(1.5)", down: "SELECT 1"})
		id, _ := f.enqueue(db, mID, rID, "apply", nil)
		jobs = append(jobs, id)
	}

	w := f.newWorker()
	w.waitDelay = 100 * time.Millisecond
	w.SetConcurrency(3)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	start := time.Now()
	for ok := 0; ok < len(jobs); {
		if time.Since(start) > 20*time.Second {
			cancel()
			t.Fatal("job belum selesai semua")
		}
		time.Sleep(50 * time.Millisecond)
		ok = 0
		for _, id := range jobs {
			if f.job(id).Status == "success" {
				ok++
			}
		}
	}
	el := time.Since(start)
	cancel()
	<-done

	// DB A berurutan → minimal 2 × 1,5 s; DB B paralel → di bawah 3 × 1,5 s
	if el < 3*time.Second || el >= 4500*time.Millisecond {
		t.Fatalf("total %v, harusnya 3–4,5 detik", el)
	}
	for _, id := range jobs {
		if s := f.job(id); s.Attempt != 1 {
			t.Fatalf("job %d attempt = %d (nunggu lock nggak boleh nambah attempt)", id, s.Attempt)
		}
	}
}

func TestDuplicateNoDoubleCommit(t *testing.T) {
	f := newFixture(t)
	db, target := f.addDB()
	if _, err := target.Exec(context.Background(), `CREATE TABLE d (x INT)`); err != nil {
		t.Fatal(err)
	}
	mID, rID := f.newMigration(db, "insert", rev{up: "INSERT INTO d VALUES (1)", down: "DELETE FROM d"})
	jobID, exec := f.enqueue(db, mID, rID, "apply", nil)
	w := f.newWorker()
	runOnce(t, w)
	wantStatus(t, f.job(jobID), "success", "success")

	// pesan yang sama dateng lagi
	f.exec(`UPDATE jobs SET status = 'queued', run_after = now() WHERE id = $1`, jobID)
	f.exec(`UPDATE change_requests SET status = 'queued' WHERE id = (SELECT change_request_id FROM jobs WHERE id = $1)`, jobID)
	if !runOnce(t, w) {
		t.Fatal("job dobel nggak ke-claim")
	}
	wantStatus(t, f.job(jobID), "success", "success")
	if n := count(t, target, `SELECT count(*) FROM d`); n != 1 {
		t.Fatalf("INSERT jalan %d kali", n)
	}
	if n := count(t, f.platform, `SELECT count(*) FROM migration_history WHERE execution_id = $1`, exec); n != 1 {
		t.Fatalf("history = %d", n)
	}
}

func TestRecoverUncertain(t *testing.T) {
	f := newFixture(t)
	db, target := f.addDB()
	if _, err := target.Exec(context.Background(), `CREATE TABLE d (x INT)`); err != nil {
		t.Fatal(err)
	}
	mID, rID := f.newMigration(db, "insert", rev{up: "INSERT INTO d VALUES (1)", down: "DELETE FROM d"})
	jobID, exec := f.enqueue(db, mID, rID, "apply", nil)
	w := f.newWorker()
	calls := 0
	w.record = func(ctx context.Context, tx pgx.Tx, r history.Result) error {
		calls++
		if calls == 1 {
			return errors.New("platform putus")
		}
		return history.Record(ctx, tx, r)
	}

	runOnce(t, w)
	s := f.job(jobID)
	wantStatus(t, s, "uncertain", "uncertain")
	if n := count(t, f.platform, `SELECT count(*) FROM migration_history`); n != 0 {
		t.Fatalf("history = %d", n)
	}
	if n := count(t, f.platform, `SELECT count(*) FROM database_locks`); n != 0 {
		t.Fatal("lock nggak dilepas")
	}

	if !runOnce(t, w) {
		t.Fatal("job uncertain nggak ke-claim")
	}
	s = f.job(jobID)
	wantStatus(t, s, "success", "success")
	if s.LastErr != nil {
		t.Fatalf("last_error nggak dikosongin: %q", *s.LastErr)
	}
	if n := count(t, target, `SELECT count(*) FROM d`); n != 1 {
		t.Fatalf("INSERT jalan %d kali", n)
	}
	if n := count(t, f.platform, `SELECT count(*) FROM migration_history WHERE execution_id = $1`, exec); n != 1 {
		t.Fatalf("history = %d", n)
	}
	if calls != 2 {
		t.Fatalf("record dipanggil %d kali", calls)
	}
}

func TestLockTakeover(t *testing.T) {
	f := newFixture(t)
	db, target := f.addDB()
	m1, r1 := f.newMigration(db, "lama", rev{up: "CREATE TABLE x1 (id INT)", down: "DROP TABLE x1"})
	old, _ := f.enqueue(db, m1, r1, "apply", nil)
	// job lama: udah mulai eksekusi (attempt 1), masih running di tabel jobs, tapi lock-nya udah kedaluwarsa (worker mati)
	f.exec(`UPDATE jobs SET status = 'running', attempt = 1, worker_id = 'mati', lease_until = now() + interval '1 hour' WHERE id = $1`, old)
	f.exec(`UPDATE change_requests SET status = 'running' WHERE id = (SELECT change_request_id FROM jobs WHERE id = $1)`, old)
	f.exec(`INSERT INTO database_locks (db_connection_id, job_id, worker_id, lease_until) VALUES ($1, $2, 'mati', now() - interval '1 minute')`, db, old)

	m2, r2 := f.newMigration(db, "baru", rev{up: "CREATE TABLE x2 (id INT)", down: "DROP TABLE x2"})
	cur, _ := f.enqueue(db, m2, r2, "apply", nil)
	w := f.newWorker()

	runOnce(t, w)
	wantStatus(t, f.job(old), "uncertain", "uncertain")
	s := f.job(cur)
	wantStatus(t, s, "queued", "queued")
	if s.Attempt != 0 {
		t.Fatalf("attempt job baru = %d", s.Attempt)
	}
	if n := count(t, f.platform, `SELECT count(*) FROM database_locks`); n != 0 {
		t.Fatal("lock harusnya dilepas")
	}
	if hasTable(t, target, "x2") {
		t.Fatal("job baru nggak boleh jalan dulu")
	}

	drain(t, w)
	wantStatus(t, f.job(old), "success", "success")
	wantStatus(t, f.job(cur), "success", "success")
	if !hasTable(t, target, "x1") || !hasTable(t, target, "x2") {
		t.Fatal("tabel nggak kebikin")
	}
}

func TestLockBusy(t *testing.T) {
	f := newFixture(t)
	db, target := f.addDB()
	m1, r1 := f.newMigration(db, "pemegang", rev{up: "SELECT 1", down: "SELECT 1"})
	holder, _ := f.enqueue(db, m1, r1, "apply", nil)
	f.exec(`UPDATE jobs SET status = 'running', worker_id = 'lain', lease_until = now() + interval '1 hour' WHERE id = $1`, holder)
	f.exec(`INSERT INTO database_locks (db_connection_id, job_id, worker_id, lease_until) VALUES ($1, $2, 'lain', now() + interval '1 minute')`, db, holder)

	m2, r2 := f.newMigration(db, "nunggu", rev{up: "CREATE TABLE w1 (id INT)", down: "DROP TABLE w1"})
	cur, _ := f.enqueue(db, m2, r2, "apply", nil)
	runOnce(t, f.newWorker())

	s := f.job(cur)
	wantStatus(t, s, "queued", "queued")
	if s.Attempt != 0 || hasTable(t, target, "w1") {
		t.Fatalf("attempt=%d, harusnya nunggu lock", s.Attempt)
	}
	if n := count(t, f.platform, `SELECT count(*) FROM database_locks WHERE job_id = $1`, holder); n != 1 {
		t.Fatal("lock pemegang nggak boleh diganggu")
	}
}

func TestHoldAndResume(t *testing.T) {
	f := newFixture(t)
	db, target := f.addDB()
	m1, r1 := f.newMigration(db, "setengah", rev{up: "CREATE TABLE p1 (id INT)", down: "DROP TABLE p1"})
	part, partExec := f.enqueue(db, m1, r1, "apply", nil)
	f.exec(`UPDATE jobs SET status = 'partial', attempt = 1 WHERE id = $1`, part)
	f.exec(`UPDATE change_requests SET status = 'partial' WHERE id = (SELECT change_request_id FROM jobs WHERE id = $1)`, part)

	m2, r2 := f.newMigration(db, "lain", rev{up: "CREATE TABLE q1 (id INT)", down: "DROP TABLE q1"})
	other, _ := f.enqueue(db, m2, r2, "apply", nil)
	w := f.newWorker()

	runOnce(t, w)
	s := f.job(other)
	wantStatus(t, s, "queued", "queued")
	if s.Attempt != 0 || hasTable(t, target, "q1") {
		t.Fatalf("job harusnya ketahan (attempt=%d)", s.Attempt)
	}

	// resume dari job partial nggak boleh ketahan sama leluhurnya sendiri
	res, resExec := f.enqueue(db, m1, r1, "resume", &partExec)
	drain(t, w)
	wantStatus(t, f.job(res), "success", "success")
	wantStatus(t, f.job(part), "success", "success")
	wantStatus(t, f.job(other), "success", "success")
	if !hasTable(t, target, "p1") || !hasTable(t, target, "q1") {
		t.Fatal("tabel nggak kebikin")
	}
	if n := count(t, target, `SELECT count(*) FROM _platform.executions WHERE execution_id = $1`, partExec); n != 1 {
		t.Fatalf("bukti resume harus pake root, dapet %d", n)
	}
	// history pake execution ID job resume-nya, dicatat sebagai apply
	if n := count(t, f.platform, `
		SELECT count(*) FROM migration_history WHERE execution_id = $1 AND action = 'apply' AND status = 'success'`, resExec); n != 1 {
		t.Fatalf("history resume = %d", n)
	}
}

func TestRevalidateFail(t *testing.T) {
	f := newFixture(t)
	db, target := f.addDB()
	mID, rID := f.newMigration(db, "dicabut", rev{up: "CREATE TABLE rv (id INT)", down: "DROP TABLE rv"})
	jobID, _ := f.enqueue(db, mID, rID, "apply", nil)
	w := f.newWorker()
	w.revalidate = func(context.Context, pgx.Tx, int64) error { return errors.New("approval sudah dicabut") }
	runOnce(t, w)

	s := f.job(jobID)
	wantStatus(t, s, "failed", "failed")
	if !strings.Contains(*s.LastErr, "approval sudah dicabut") {
		t.Fatalf("last_error = %q", *s.LastErr)
	}
	if hasTable(t, target, "rv") {
		t.Fatal("target nggak boleh kesentuh")
	}
	if n := count(t, f.platform, `SELECT count(*) FROM database_locks`); n != 0 {
		t.Fatal("lock nggak dilepas")
	}
}

func TestRollback(t *testing.T) {
	f := newFixture(t)
	db, target := f.addDB()
	mID, rID := f.newMigration(db, "rb", rev{up: "CREATE TABLE rb (id INT)", down: "DROP TABLE rb"})
	_, applyExec := f.enqueue(db, mID, rID, "apply", nil)
	w := f.newWorker()
	runOnce(t, w)
	if !hasTable(t, target, "rb") {
		t.Fatal("apply gagal")
	}

	rbJob, rbExec := f.enqueue(db, mID, rID, "rollback", &applyExec)
	runOnce(t, w)
	wantStatus(t, f.job(rbJob), "success", "success")
	if hasTable(t, target, "rb") {
		t.Fatal("SQL down nggak jalan")
	}
	if n := count(t, target, `SELECT count(*) FROM _platform.executions WHERE execution_id = $1 AND direction = 'down'`, rbExec); n != 1 {
		t.Fatalf("bukti rollback = %d", n)
	}
	if n := count(t, f.platform, `
		SELECT count(*) FROM migration_history
		WHERE execution_id = $1 AND action = 'rollback' AND reverts_execution_id = $2`, rbExec, applyExec); n != 1 {
		t.Fatalf("history rollback = %d", n)
	}
	var applied bool
	if err := f.platform.QueryRow(context.Background(), `SELECT is_applied FROM migrations WHERE id = $1`, mID).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("is_applied harusnya false")
	}
}

func TestLockLostAborts(t *testing.T) {
	f := newFixture(t)
	db, target := f.addDB()
	mID, rID := f.newMigration(db, "lama", rev{up: "CREATE TABLE ll (id INT); SELECT pg_sleep(3)", down: "DROP TABLE ll"})
	jobID, exec := f.enqueue(db, mID, rID, "apply", nil)
	w := f.newWorker()
	w.beat = 50 * time.Millisecond

	go func() {
		// lock dicolong pas SQL lagi jalan
		time.Sleep(500 * time.Millisecond)
		f.platform.Exec(context.Background(), `DELETE FROM database_locks WHERE job_id = $1`, jobID)
	}()
	start := time.Now()
	runOnce(t, w)
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Fatalf("job nggak dibatalin pas lock ilang (%v)", d)
	}
	s := f.job(jobID)
	if s.Status != "running" || s.Attempt != 1 {
		t.Fatalf("job abort harus dibiarin running, dapet %s attempt %d", s.Status, s.Attempt)
	}
	if hasTable(t, target, "ll") {
		t.Fatal("transaksi target harusnya batal")
	}
	if n := count(t, target, `SELECT count(*) FROM _platform.executions WHERE execution_id = $1`, exec); n != 0 {
		t.Fatal("bukti eksekusi nggak boleh ada")
	}
}

// pemegang lock yang mati sebelum sempet mulai (attempt 0) cukup dibalikin ke antrean,
// job kita langsung lanjut
func TestLockTakeoverNotStarted(t *testing.T) {
	f := newFixture(t)
	db, target := f.addDB()
	m1, r1 := f.newMigration(db, "lama", rev{up: "CREATE TABLE y1 (id INT)", down: "DROP TABLE y1"})
	old, _ := f.enqueue(db, m1, r1, "apply", nil)
	f.exec(`UPDATE jobs SET status = 'running', worker_id = 'mati', lease_until = now() + interval '1 hour' WHERE id = $1`, old)
	f.exec(`INSERT INTO database_locks (db_connection_id, job_id, worker_id, lease_until) VALUES ($1, $2, 'mati', now() - interval '1 minute')`, db, old)

	m2, r2 := f.newMigration(db, "baru", rev{up: "CREATE TABLE y2 (id INT)", down: "DROP TABLE y2"})
	cur, _ := f.enqueue(db, m2, r2, "apply", nil)
	w := f.newWorker()

	runOnce(t, w)
	wantStatus(t, f.job(old), "queued", "queued")
	wantStatus(t, f.job(cur), "success", "success")
	if !hasTable(t, target, "y2") {
		t.Fatal("job baru harusnya langsung jalan")
	}
	drain(t, w)
	wantStatus(t, f.job(old), "success", "success")
}
