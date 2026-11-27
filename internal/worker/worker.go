package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"
	"github.com/fahdmz/K03-05-WBD/internal/history"
	"github.com/fahdmz/K03-05-WBD/internal/obs"
	"github.com/fahdmz/K03-05-WBD/internal/queue"
	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/fahdmz/K03-05-WBD/internal/workflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
)

const finishTimeout = 15 * time.Second

var errLockLost = errors.New("lock database hilang")

type Worker struct {
	platform     *pgxpool.Pool
	manager      dbmanager.Manager
	id           string
	pollInterval time.Duration
	concurrency  int
	lease        time.Duration
	beat         time.Duration
	waitDelay    time.Duration // jeda balik antre kalau lock sibuk / ketahan

	// bisa diganti di test
	revalidate func(ctx context.Context, tx pgx.Tx, crID int64) error
	buildPlan  func(ctx context.Context, q store.Querier, crID int64) (history.Plan, error)
	record     func(ctx context.Context, tx pgx.Tx, r history.Result) error
	backoff    func(attempt int) time.Duration

	mx *Metrics // nil = metrics mati
}

func New(platform *pgxpool.Pool, manager dbmanager.Manager) *Worker {
	return &Worker{
		platform:     platform,
		manager:      manager,
		id:           newID(),
		pollInterval: time.Second,
		concurrency:  4,
		lease:        leaseFor,
		beat:         heartbeat,
		waitDelay:    2 * time.Second,
		revalidate:   workflow.Revalidate,
		buildPlan:    history.BuildPlan,
		record:       history.Record,
		backoff:      queue.Backoff,
	}
}

func (w *Worker) ID() string {
	return w.id
}

func (w *Worker) SetConcurrency(n int) {
	if n > 0 {
		w.concurrency = n
	}
}

// Run jalanin `concurrency` loop claim → proses sampe ctx selesai
func (w *Worker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < w.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.loop(ctx)
		}()
	}
	wg.Wait()
}

func (w *Worker) loop(ctx context.Context) {
	for ctx.Err() == nil {
		got, err := w.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			obs.Logger(ctx).Error("worker.claim_failed", "worker_id", w.id, "error", obs.Redact(err.Error()))
		}
		if got && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.pollInterval):
		}
	}
}

// RunOnce: satu claim + proses. false kalau antrean kosong.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	c, err := queue.Claim(ctx, w.platform, w.id, w.lease)
	if err != nil || c == nil {
		return false, err
	}
	w.process(ctx, job{
		ID:          c.ID,
		CRID:        c.CRID,
		DBID:        c.DBID,
		ExecID:      c.ExecID,
		PrevStatus:  c.PrevStatus,
		Attempt:     c.Attempt,
		MaxAttempts: c.MaxAttempts,
		Trace:       c.Trace,
		First:       c.First,
		Progress:    c.Progress,
		Waited:      c.Waited,
	})
	return true, nil
}

type meta struct {
	migID int64
	actor string
}

func (w *Worker) meta(ctx context.Context, crID int64) (meta, error) {
	var m meta
	err := w.platform.QueryRow(ctx, `
		SELECT cr.migration_id, u.username
		FROM change_requests cr JOIN users u ON u.id = cr.requested_by
		WHERE cr.id = $1`, crID,
	).Scan(&m.migID, &m.actor)
	if err != nil {
		return m, fmt.Errorf("baca change request %d: %w", crID, err)
	}
	return m, nil
}

func (w *Worker) logCtx(ctx context.Context, j job, m meta, exec string, attempt int) context.Context {
	return obs.With(ctx, "job_id", j.ID, "execution_id", exec, "database", j.DBID,
		"migration_id", m.migID, "attempt", attempt, "actor", m.actor, "worker_id", w.id)
}

func (w *Worker) process(ctx context.Context, j job) {
	began := time.Now()
	ctx = obs.ExtractTrace(ctx, j.Trace)
	ctx, span := obs.Start(ctx, "worker.job",
		attribute.Int64("job.id", j.ID), attribute.Int64("db.id", j.DBID), attribute.String("job.execution_id", j.ExecID))
	var spanErr error
	defer func() { obs.End(span, spanErr) }()

	base, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	m, err := w.meta(base, j.CRID)
	if err != nil {
		spanErr = err
		obs.Logger(base).Error("job.claim_failed", "job_id", j.ID, "error", obs.Redact(err.Error()))
		w.requeue(base, j, w.waitDelay)
		return
	}
	ctx = w.logCtx(base, j, m, j.ExecID, j.Attempt)
	obs.Logger(ctx).Info("job.claimed", "status", "running", "prev_status", j.PrevStatus)
	if j.First {
		w.mx.waited(j.Waited)
	}

	got, err := w.lock(ctx, j)
	if err != nil || !got {
		spanErr = err
		return
	}
	stopHB := w.heartbeat(ctx, j, cancel)

	// keluar sebelum eksekusi: lepas lock, balik antre
	leave := func(delay time.Duration) {
		stopHB()
		if w.requeue(ctx, j, delay) {
			w.release(ctx, j)
		}
	}
	finish := func(r *run, o outcome) {
		stopHB()
		if o.err != nil {
			spanErr = o.err
		}
		if o.kind == okAbort {
			w.mx.jobDone(r, "aborted", began)
			obs.Logger(ctx).Warn("job.aborted", "status", "running", "reason", fmt.Sprint(context.Cause(ctx)))
			return
		}
		if w.finalize(ctx, r, o, began) {
			w.release(ctx, j)
		}
	}
	aborted := func() bool {
		if ctx.Err() == nil {
			return false
		}
		finish(nil, outcome{kind: okAbort})
		return true
	}

	if by, st, err := w.heldBy(ctx, j); err != nil || by != 0 {
		if aborted() {
			return
		}
		if err != nil {
			spanErr = err
			obs.Logger(ctx).Error("job.hold_check_failed", "error", obs.Redact(err.Error()))
		} else {
			obs.Logger(ctx).Info("job.held", "status", "queued", "blocked_by", by, "blocked_status", st)
		}
		leave(w.waitDelay)
		return
	}

	// pesan dobel: eksekusi ini udah kecatat di history
	if dup, err := recorded(ctx, w.platform, j.ExecID); err != nil || dup {
		if aborted() {
			return
		}
		if err != nil {
			spanErr = err
			leave(w.waitDelay)
			return
		}
		obs.Logger(ctx).Info("job.duplicate", "status", "success")
		finish(&run{job: j, root: j.ExecID, attempt: j.Attempt}, outcome{kind: okDone})
		return
	}

	careful := j.careful()
	// gagal sebelum bukti di target kebaca, padahal attempt sebelumnya mungkin udah commit → uncertain
	unsure := func(r *run, err error) {
		spanErr = err
		r.attempt = j.Attempt + 1
		finish(r, outcome{kind: okUncertain, err: err})
	}
	// gangguan sementara di platform: balik antre aja, jangan di-failed
	transient := func(event string, err error) bool {
		if !queue.IsTransient(err) {
			return false
		}
		spanErr = err
		obs.Logger(ctx).Warn(event, "error", obs.Redact(err.Error()))
		leave(w.waitDelay)
		return true
	}

	// revalidasi tepat sebelum eksekusi, selama belum ada progres di target
	if !careful {
		if err := w.doRevalidate(ctx, j.CRID); err != nil {
			if aborted() || transient("revalidation.retry", err) {
				return
			}
			obs.Logger(ctx).Warn("revalidation.failed", "status", "failed", "error", obs.Redact(err.Error()))
			r := &run{job: j, root: j.ExecID, attempt: j.Attempt}
			finish(r, outcome{kind: okFail, err: fmt.Errorf("Validasi ulang gagal: %w", err)})
			return
		}
	}

	plan, err := w.buildPlan(ctx, w.platform, j.CRID)
	if err != nil {
		if aborted() {
			return
		}
		if queue.IsTransient(err) {
			spanErr = err
			obs.Logger(ctx).Warn("job.plan_failed", "error", obs.Redact(err.Error()))
			leave(w.waitDelay)
			return
		}
		r := &run{job: j, root: j.ExecID, attempt: j.Attempt}
		e := fmt.Errorf("Rencana eksekusi gagal dibuat: %w", err)
		if careful {
			unsure(r, e)
			return
		}
		finish(r, outcome{kind: okFail, err: e})
		return
	}

	r := &run{job: j, plan: plan, platform: w.platform, attempt: j.Attempt}
	ri, err := findRoot(ctx, w.platform, j.ExecID, plan)
	if err != nil {
		if aborted() {
			return
		}
		r.root = j.ExecID
		if transient("job.plan_failed", err) {
			return
		}
		e := fmt.Errorf("Rencana eksekusi gagal dibuat: %w", err)
		if careful {
			unsure(r, e)
			return
		}
		finish(r, outcome{kind: okFail, err: e})
		return
	}
	r.root, r.rootAction, r.rootTarget = ri.exec, ri.action, ri.target

	if !careful {
		if err := w.checkTarget(ctx, r); err != nil {
			if aborted() || transient("job.target_check_retry", err) {
				return
			}
			r.attempt = j.Attempt
			finish(r, outcome{kind: okFail, err: err})
			return
		}
	}

	// worker beda proses sama server, jadi data koneksi selalu dibaca ulang
	w.manager.Invalidate(j.DBID)
	target, err := w.manager.Pool(ctx, j.DBID)
	if err == nil {
		err = ensurePlatform(ctx, target)
	}
	if err != nil {
		if aborted() {
			return
		}
		e := fmt.Errorf("gagal terhubung ke database target: %w", err)
		if careful {
			unsure(r, e)
			return
		}
		r.attempt = j.Attempt + 1
		finish(r, classify(ctx, e, outcome{}))
		return
	}
	r.target = target

	ok, err := w.begin(ctx, r)
	if err != nil || !ok {
		if aborted() {
			return
		}
		if err != nil {
			spanErr = err
			obs.Logger(ctx).Error("job.start_failed", "error", obs.Redact(err.Error()))
			leave(w.waitDelay)
			return
		}
		// job udah bukan punya kita (diambil alih / ditandai uncertain)
		stopHB()
		w.release(ctx, j)
		obs.Logger(ctx).Warn("job.lost")
		return
	}
	ctx = w.logCtx(base, j, m, r.root, r.attempt)
	r.progress = w.progress(j)
	dir := r.dir()
	r.onBatch = func(rows int64) { w.mx.batch(dir, 1, rows) }
	obs.Logger(ctx).Info("job.started", "status", "running", "action", string(plan.Action), "mode", plan.Mode,
		"direction", r.dir(), "job_execution_id", j.ExecID)

	var o outcome
	if plan.Mode == "batch" {
		o = runBatch(ctx, r)
	} else {
		o = runNormal(ctx, r)
	}
	finish(r, o)
}

// checkTarget: resume/revert cuma boleh dari job yang masih partial. Bisa aja job tujuan
// udah diberesin job lain (resume/revert kembarannya) sejak request ini dibikin.
func (w *Worker) checkTarget(ctx context.Context, r *run) error {
	a := r.plan.Action
	if a != history.Resume && a != history.Revert {
		return nil
	}
	if r.plan.TargetExecutionID == nil {
		return fmt.Errorf("%s butuh job tujuan", a)
	}
	var id int64
	var st string
	err := w.platform.QueryRow(ctx, `
		SELECT id, status FROM jobs WHERE execution_id = $1`, *r.plan.TargetExecutionID,
	).Scan(&id, &st)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("Job tujuan tidak ditemukan")
	}
	if err != nil {
		return fmt.Errorf("cek job tujuan: %w", err)
	}
	if st != "partial" {
		return fmt.Errorf("Job tujuan #%d sudah tidak partial (status %s), %s dibatalkan", id, st, a)
	}
	return nil
}

func (w *Worker) doRevalidate(ctx context.Context, crID int64) (err error) {
	ctx, span := obs.Start(ctx, "revalidate", attribute.Int64("change_request.id", crID))
	defer func() { obs.End(span, err) }()
	tx, err := w.platform.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin revalidasi: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := w.revalidate(ctx, tx, crID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// begin: attempt++ dan CR jadi running, satu tx. false = job udah bukan punya kita.
func (w *Worker) begin(ctx context.Context, r *run) (bool, error) {
	tx, err := w.platform.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin mulai job: %w", err)
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `
		UPDATE jobs SET attempt = attempt + 1
		WHERE id = $1 AND worker_id = $2 AND status = 'running'
		RETURNING attempt`, r.job.ID, w.id,
	).Scan(&r.attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("naikin attempt: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE change_requests SET status = 'running', updated_at = now() WHERE id = $1`, r.job.CRID,
	); err != nil {
		return false, fmt.Errorf("update status change request: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit mulai job: %w", err)
	}
	return true, nil
}

func (w *Worker) progress(j job) func(ctx context.Context, batches int, rows int64) {
	return func(ctx context.Context, batches int, rows int64) {
		if _, err := w.platform.Exec(ctx, `
			UPDATE jobs SET batches_committed = $2, rows_committed = $3
			WHERE id = $1 AND worker_id = $4 AND status = 'running'`,
			j.ID, batches, rows, w.id,
		); err != nil {
			obs.Logger(ctx).Warn("job.progress_failed", "error", obs.Redact(err.Error()))
		}
	}
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	host, _ := os.Hostname()
	return host + "-" + hex.EncodeToString(b)
}
