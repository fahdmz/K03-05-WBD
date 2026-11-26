package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/fahdmz/K03-05-WBD/internal/history"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	upSQL   = `UPDATE items SET val = val + 10 / div WHERE id > {{lo}} AND id <= {{hi}}`
	downSQL = `UPDATE items SET val = val - 10 / div WHERE id > {{lo}} AND id <= {{hi}}`
)

// bfix: fixture batch, tabel items (id SERIAL, val, div) di database target
type bfix struct {
	*fixture
	dbID     int64
	tgt      *pgxpool.Pool
	mID, rID int64
	seen     [][2]int64 // progress (batches, rows)
}

func newBfix(t *testing.T, n int) *bfix {
	t.Helper()
	f := newFixture(t)
	b := &bfix{fixture: f}
	b.dbID, b.tgt = f.addDB()
	b.exec(`
		CREATE TABLE items (id SERIAL PRIMARY KEY, val INT NOT NULL DEFAULT 0, div INT NOT NULL DEFAULT 1);
		INSERT INTO items (val) SELECT 0 FROM generate_series(1, ` + fmt.Sprint(n) + `)`)
	if err := ensurePlatform(context.Background(), b.tgt); err != nil {
		t.Fatal(err)
	}
	b.mID, b.rID = f.newMigration(b.dbID, "batch items", rev{
		up: upSQL, down: downSQL, mode: "batch", table: "public.items", col: "id", size: 100,
	})
	return b
}

func (b *bfix) exec(sql string, args ...any) {
	b.t.Helper()
	if _, err := b.tgt.Exec(context.Background(), sql, args...); err != nil {
		b.t.Fatalf("exec target %q: %v", sql, err)
	}
}

func (b *bfix) start(action string, target *string) string {
	b.t.Helper()
	_, execID := b.enqueue(b.dbID, b.mID, b.rID, action, target)
	return execID
}

// newRun rakit run kayak worker: BuildPlan + cari root
func (b *bfix) newRun(execID string, attempt int) *run {
	b.t.Helper()
	ctx := context.Background()
	var crID int64
	if err := b.platform.QueryRow(ctx, `SELECT change_request_id FROM jobs WHERE execution_id = $1`, execID).Scan(&crID); err != nil {
		b.t.Fatalf("cari cr: %v", err)
	}
	plan, err := history.BuildPlan(ctx, b.platform, crID)
	if err != nil {
		b.t.Fatalf("build plan: %v", err)
	}
	ri, err := findRoot(ctx, b.platform, execID, plan)
	if err != nil {
		b.t.Fatalf("cari root: %v", err)
	}
	r := &run{plan: plan, root: ri.exec, rootAction: ri.action, rootTarget: ri.target,
		attempt: attempt, target: b.tgt, platform: b.platform}
	b.seen = nil
	r.progress = func(_ context.Context, n int, rows int64) {
		b.seen = append(b.seen, [2]int64{int64(n), rows})
	}
	return r
}

// vals: jumlah row val=10, val=0, dan total val
func (b *bfix) vals(where string) (ten, zero int, sum int64) {
	b.t.Helper()
	if where == "" {
		where = "true"
	}
	if err := b.tgt.QueryRow(context.Background(), `
		SELECT count(*) FILTER (WHERE val = 10), count(*) FILTER (WHERE val = 0), COALESCE(sum(val), 0)
		FROM items WHERE `+where).Scan(&ten, &zero, &sum); err != nil {
		b.t.Fatalf("baca items: %v", err)
	}
	return
}

// ckpts: batch_no urut committed_at
func (b *bfix) ckpts(root, dir string) []int {
	b.t.Helper()
	rows, err := b.tgt.Query(context.Background(), `
		SELECT batch_no FROM _platform.batch_checkpoints WHERE execution_id = $1 AND direction = $2
		ORDER BY committed_at, batch_no`, root, dir)
	if err != nil {
		b.t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var n int
		rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

func (b *bfix) execAttempt(root, dir string) int {
	b.t.Helper()
	var a int
	err := b.tgt.QueryRow(context.Background(), `
		SELECT COALESCE((SELECT attempt FROM _platform.executions WHERE execution_id = $1 AND direction = $2), 0)`,
		root, dir).Scan(&a)
	if err != nil {
		b.t.Fatal(err)
	}
	return a
}

// record: catat apply ke history (biar rollback lolos CheckRollback)
func (b *bfix) record(r *run, o outcome) {
	b.t.Helper()
	ctx := context.Background()
	tx, err := b.platform.Begin(ctx)
	if err != nil {
		b.t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := history.Record(ctx, tx, history.Result{
		Plan: r.plan, ExecutionID: r.root, Attempt: max(r.attempt, 1), Status: history.Succeeded,
		ExecutedSQL: o.executedSQL, Batches: o.batches,
		RowsAffected: o.rows, StartedAt: o.startedAt, FinishedAt: o.committedAt,
	}); err != nil {
		b.t.Fatalf("record: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		b.t.Fatal(err)
	}
}

func wantOut(t *testing.T, o outcome, kind outcomeKind, batches int, rows int64) {
	t.Helper()
	if o.kind != kind || o.batches != batches || o.rows != rows {
		t.Fatalf("outcome = %s batches %d rows %d (err %v), want %s %d %d",
			o.kind, o.batches, o.rows, o.err, kind, batches, rows)
	}
}

func wantSeq(t *testing.T, name string, got, want []int) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func seq(from, to int) []int {
	var out []int
	step := 1
	if to < from {
		step = -1
	}
	for i := from; ; i += step {
		out = append(out, i)
		if i == to {
			return out
		}
	}
}

func applyAll(t *testing.T, b *bfix) (string, *run, outcome) {
	t.Helper()
	e := b.start("apply", nil)
	r := b.newRun(e, 1)
	o := runBatch(context.Background(), r)
	wantOut(t, o, okDone, 10, 1000)
	return e, r, o
}

func TestBatchScopeFixed(t *testing.T) {
	b := newBfix(t, 1000)
	e := b.start("apply", nil)
	r := b.newRun(e, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.progress = func(_ context.Context, n int, _ int64) {
		if n == 1 {
			b.exec(`INSERT INTO items (val) SELECT 0 FROM generate_series(1, 100)`)
		}
		if n == 3 {
			cancel()
		}
	}
	wantOut(t, runBatch(ctx, r), okAbort, 3, 300)

	// row baru lagi di antara dua panggilan
	b.exec(`INSERT INTO items (val) SELECT 0 FROM generate_series(1, 50)`)
	o := runBatch(context.Background(), b.newRun(e, 2))
	wantOut(t, o, okDone, 10, 1000)
	if ten, _, _ := b.vals("id <= 1000"); ten != 1000 {
		t.Fatalf("row lama yang ke-update = %d, want 1000", ten)
	}
	if _, zero, _ := b.vals("id > 1000"); zero != 150 {
		t.Fatalf("row baru yang nggak disentuh = %d, want 150", zero)
	}
}

func TestBatchCrashResume(t *testing.T) {
	b := newBfix(t, 1000)
	e := b.start("apply", nil)
	r := b.newRun(e, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.progress = func(_ context.Context, n int, _ int64) {
		if n == 4 {
			cancel()
		}
	}
	o := runBatch(ctx, r)
	wantOut(t, o, okAbort, 4, 400)
	if !errors.Is(o.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", o.err)
	}
	if b.execAttempt(e, "up") != 0 {
		t.Fatal("executions nggak boleh ada sebelum selesai")
	}

	// worker lain lanjut (attempt baru, root sama)
	o = runBatch(context.Background(), b.newRun(e, 2))
	wantOut(t, o, okDone, 10, 1000)
	if len(b.seen) != 6 || b.seen[0] != [2]int64{5, 500} {
		t.Fatalf("progress lanjutan = %v", b.seen)
	}
	if ten, _, sum := b.vals(""); ten != 1000 || sum != 10000 {
		t.Fatalf("dobel/kurang: val=10 %d row, sum %d", ten, sum)
	}
	if a := b.execAttempt(e, "up"); a != 2 {
		t.Fatalf("executions attempt = %d, want 2", a)
	}
}

func TestBatchDuplicate(t *testing.T) {
	b := newBfix(t, 1000)
	e, _, _ := applyAll(t, b)
	o := runBatch(context.Background(), b.newRun(e, 2))
	wantOut(t, o, okDone, 10, 1000)
	if len(b.seen) != 0 || o.committedAt.IsZero() {
		t.Fatalf("pesan duplikat nggak boleh jalanin batch: progress %v", b.seen)
	}
	if _, _, sum := b.vals(""); sum != 10000 {
		t.Fatalf("sum = %d, want 10000", sum)
	}
	if a := b.execAttempt(e, "up"); a != 1 {
		t.Fatalf("executions attempt = %d, want tetap 1", a)
	}
}

func TestBatchFirstBatchFails(t *testing.T) {
	b := newBfix(t, 1000)
	b.exec(`UPDATE items SET div = 0 WHERE id = 5`)
	e := b.start("apply", nil)
	o := runBatch(context.Background(), b.newRun(e, 1))
	wantOut(t, o, okFail, 0, 0)
	if o.sqlstate != "22012" {
		t.Fatalf("sqlstate = %q", o.sqlstate)
	}
}

func TestBatchTransientRetry(t *testing.T) {
	b := newBfix(t, 1000)
	b.exec(`
		CREATE TABLE flaky (id INT);
		INSERT INTO flaky VALUES (350);
		CREATE FUNCTION chk(i INT) RETURNS INT LANGUAGE plpgsql AS $$
		BEGIN
			IF EXISTS (SELECT 1 FROM flaky WHERE id = i) THEN
				RAISE EXCEPTION 'konflik' USING ERRCODE = '40001';
			END IF;
			RETURN 10;
		END $$`)
	b.mID, b.rID = b.newMigration(b.dbID, "flaky", rev{
		up:   `UPDATE items SET val = val + chk(id) WHERE id > {{lo}} AND id <= {{hi}}`,
		down: downSQL, mode: "batch", table: "items", col: "id", size: 100,
	})
	e := b.start("apply", nil)
	o := runBatch(context.Background(), b.newRun(e, 1))
	wantOut(t, o, okRetry, 3, 300)
	if o.sqlstate != "40001" {
		t.Fatalf("sqlstate = %q", o.sqlstate)
	}
	b.exec(`DELETE FROM flaky`)
	wantOut(t, runBatch(context.Background(), b.newRun(e, 2)), okDone, 10, 1000)
	if _, _, sum := b.vals(""); sum != 10000 {
		t.Fatalf("sum = %d", sum)
	}
}

func TestBatchRevertReverse(t *testing.T) {
	b := newBfix(t, 1000)
	e, _, _ := applyAll(t, b)

	b.exec(`UPDATE items SET div = 0 WHERE id = 350`)
	rv := b.start("revert", &e)
	r := b.newRun(rv, 1)
	if r.root != e || r.plan.Direction != history.Down || r.plan.SQL != downSQL {
		t.Fatalf("revert root %s arah %s", r.root, r.plan.Direction)
	}
	o := runBatch(context.Background(), r)
	wantOut(t, o, okPartial, 6, 600)
	wantSeq(t, "checkpoint down", b.ckpts(e, "down"), seq(10, 5))
	if ten, zero, _ := b.vals(""); ten != 400 || zero != 600 {
		t.Fatalf("setelah revert parsial: val=10 %d, val=0 %d", ten, zero)
	}

	b.exec(`UPDATE items SET div = 1`)
	res := b.start("resume", &rv)
	r = b.newRun(res, 1)
	if r.root != e || r.plan.Direction != history.Down {
		t.Fatalf("resume revert root %s arah %s, want %s down", r.root, r.plan.Direction, e)
	}
	o = runBatch(context.Background(), r)
	wantOut(t, o, okDone, 10, 1000)
	wantSeq(t, "checkpoint down", b.ckpts(e, "down"), seq(10, 1))
	if _, zero, _ := b.vals(""); zero != 1000 {
		t.Fatalf("val=0 %d, want 1000", zero)
	}
	if b.execAttempt(e, "down") != 1 {
		t.Fatal("executions down belum ada")
	}
	var bad int
	b.tgt.QueryRow(context.Background(), `
		SELECT count(*) FROM _platform.batch_checkpoints u
		JOIN _platform.batch_checkpoints d USING (execution_id, batch_no)
		WHERE u.execution_id = $1 AND u.direction = 'up' AND d.direction = 'down'
		  AND (u.lo, u.hi) IS DISTINCT FROM (d.lo, d.hi)`, e).Scan(&bad)
	if bad != 0 {
		t.Fatalf("%d batch down rentangnya beda dari up", bad)
	}
}

func TestBatchRevertPartialApplyGuard(t *testing.T) {
	b := newBfix(t, 1000)
	b.exec(`UPDATE items SET div = 0 WHERE id = 550`)
	e := b.start("apply", nil)
	wantOut(t, runBatch(context.Background(), b.newRun(e, 1)), okPartial, 5, 500)

	// revert dari apply parsial, gagal di batch 3
	b.exec(`UPDATE items SET div = 0 WHERE id = 250`)
	rv := b.start("revert", &e)
	wantOut(t, runBatch(context.Background(), b.newRun(rv, 1)), okPartial, 2, 200)
	b.exec(`UPDATE items SET div = 1`)

	// resume apply padahal udah di-revert sebagian → ditolak
	res := b.start("resume", &e)
	o := runBatch(context.Background(), b.newRun(res, 1))
	if o.kind != okFail || !errors.Is(o.err, errRevertedUp) {
		t.Fatalf("outcome = %s %v, want failed %v", o.kind, o.err, errRevertedUp)
	}
	wantSeq(t, "checkpoint up", b.ckpts(e, "up"), seq(1, 5))

	res2 := b.start("resume", &rv)
	wantOut(t, runBatch(context.Background(), b.newRun(res2, 1)), okDone, 5, 500)
	wantSeq(t, "checkpoint down", b.ckpts(e, "down"), seq(5, 1))
	if _, zero, _ := b.vals(""); zero != 1000 {
		t.Fatalf("val=0 %d, want 1000", zero)
	}
}

func TestBatchRevertRollback(t *testing.T) {
	b := newBfix(t, 1000)
	e, ra, oa := applyAll(t, b)
	b.record(ra, oa)

	b.exec(`UPDATE items SET div = 0 WHERE id = 550`)
	rb := b.start("rollback", &e)
	wantOut(t, runBatch(context.Background(), b.newRun(rb, 1)), okPartial, 4, 400)
	b.exec(`UPDATE items SET div = 1`)

	rv := b.start("revert", &rb)
	r := b.newRun(rv, 1)
	if r.root != rb || r.rootAction != history.Rollback || r.plan.Direction != history.Up {
		t.Fatalf("revert rollback: root %s aksi %s arah %s", r.root, r.rootAction, r.plan.Direction)
	}
	wantOut(t, runBatch(context.Background(), r), okDone, 4, 400)
	wantSeq(t, "checkpoint up revert", b.ckpts(rb, "up"), seq(7, 10))
	if ten, _, _ := b.vals(""); ten != 1000 {
		t.Fatalf("val=10 %d, want 1000", ten)
	}
}

func TestBatchRejectSQL(t *testing.T) {
	bt := &history.Batch{Table: "items", Column: "id", Size: 100}
	cases := map[string]history.Plan{
		"commit":      {SQL: `UPDATE items SET val = 1 WHERE id > {{lo}} AND id <= {{hi}}; COMMIT`, Batch: bt},
		"dua stmt":    {SQL: `UPDATE items SET val = 1 WHERE id > {{lo}} AND id <= {{hi}}; DELETE FROM items WHERE id > {{lo}} AND id <= {{hi}}`, Batch: bt},
		"tanpa lo":    {SQL: `UPDATE items SET val = 1 WHERE id <= {{hi}}`, Batch: bt},
		"tanpa batch": {SQL: `UPDATE items SET val = 1 WHERE id > {{lo}} AND id <= {{hi}}`},
	}
	for name, p := range cases {
		p.Direction = history.Up
		o := runBatch(context.Background(), &run{plan: p, root: "x", rootAction: history.Apply})
		if o.kind != okFail || o.err == nil {
			t.Errorf("%s: outcome = %s %v, want failed", name, o.kind, o.err)
		}
	}
}
