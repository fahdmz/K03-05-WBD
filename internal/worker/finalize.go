package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/history"
	"github.com/fahdmz/K03-05-WBD/internal/obs"
	"github.com/jackc/pgx/v5"
)

var errNotOwned = errors.New("job sudah bukan milik worker ini")

// finalize nulis status akhir. Balikin false kalau status gagal ditulis: lock jangan dilepas,
// biar lease habis dan job dipulihin lewat jalur recovery.
func (w *Worker) finalize(ctx context.Context, r *run, o outcome, began time.Time) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	log := obs.Logger(ctx)
	msg := errMsg(o.err)
	dur := float64(time.Since(began).Microseconds()) / 1000

	var err error
	switch o.kind {
	case okDone:
		if err = w.done(ctx, r, o); err != nil && !errors.Is(err, errNotOwned) {
			log.Error("job.finalize_failed", "error", obs.Redact(err.Error()))
			return w.markUncertain(ctx, r, o, fmt.Errorf("SQL sudah commit di target tapi gagal dicatat: %w", err))
		}
		if err == nil {
			log.Info("job.finished", "status", "success", "rows", o.rows, "batches", o.batches, "duration_ms", dur)
			w.mx.jobDone(r, "success", began)
		}
	case okRetry:
		if r.attempt >= r.job.MaxAttempts {
			st := "failed"
			if o.batches > 0 {
				st = "partial"
			}
			msg = fmt.Sprintf("Gagal setelah %d percobaan: %s", r.attempt, msg)
			if err = w.end(ctx, r, o, st, -1, msg, history.Status(st)); err == nil {
				log.Error("job.finished", "status", st, "error", msg, "sqlstate", o.sqlstate, "duration_ms", dur)
				w.mx.jobDone(r, st, began)
			}
			break
		}
		d := w.backoff(r.attempt)
		if err = w.end(ctx, r, o, "queued", d, msg, history.Failed); err == nil {
			log.Warn("job.retry_scheduled", "status", "queued", "delay_ms", d.Milliseconds(), "error", msg,
				"sqlstate", o.sqlstate, "duration_ms", dur)
			w.mx.retry(r)
		}
	case okFail, okPartial:
		st := "failed"
		if o.kind == okPartial {
			st = "partial"
		}
		if err = w.end(ctx, r, o, st, -1, msg, history.Status(st)); err == nil {
			log.Error("job.finished", "status", st, "error", msg, "sqlstate", o.sqlstate,
				"rows", o.rows, "batches", o.batches, "duration_ms", dur)
			w.mx.jobDone(r, st, began)
		}
	case okUncertain:
		w.mx.jobDone(r, "uncertain", began)
		return w.markUncertain(ctx, r, o, o.err)
	}
	if errors.Is(err, errNotOwned) {
		log.Warn("job.lost", "outcome", o.kind.String())
	} else if err != nil {
		log.Error("job.finalize_failed", "outcome", o.kind.String(), "error", obs.Redact(err.Error()))
		return false
	}
	return true
}

// done: catat history (kalau belum) + job & CR success, satu tx platform
func (w *Worker) done(ctx context.Context, r *run, o outcome) error {
	tx, err := w.platform.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin finalize: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := w.setStatus(ctx, tx, r, o, "success", -1, ""); err != nil {
		return err
	}
	if r.recordable() {
		dup, err := recorded(ctx, tx, r.job.ExecID)
		if err != nil {
			return err
		}
		if !dup {
			if err := w.record(ctx, tx, r.result(o, history.Succeeded, "")); err != nil {
				return fmt.Errorf("catat history: %w", err)
			}
		}
	}
	if a := r.plan.Action; a == history.Resume || a == history.Revert {
		if err := closeChain(ctx, tx, r); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit finalize: %w", err)
	}
	return nil
}

// recordable: attempt beneran dijalanin (bukan gagal sebelum mulai)
func (r *run) recordable() bool {
	return r != nil && r.plan.Action != "" && r.attempt >= 1
}

// result: baris migration_history buat attempt ini (spek rev 4: semua attempt dicatat)
func (r *run) result(o outcome, st history.Status, msg string) history.Result {
	now := time.Now()
	res := history.Result{
		Plan: r.plan, ExecutionID: r.job.ExecID, Attempt: r.attempt, Status: st,
		ExecutedSQL: o.executedSQL, Batches: o.batches, RowsAffected: o.rows,
		ErrorMessage: msg, SQLState: o.sqlstate, StartedAt: o.startedAt, FinishedAt: o.committedAt,
	}
	if res.ExecutedSQL == "" {
		res.ExecutedSQL = r.plan.SQL
	}
	if res.StartedAt.IsZero() {
		res.StartedAt = now
	}
	if res.FinishedAt.IsZero() || st != history.Succeeded {
		res.FinishedAt = now
	}
	return res
}

func naturalDir(a history.Action) history.Direction {
	if a == history.Rollback {
		return history.Down
	}
	return history.Up
}

// end: update job & CR barengan, plus catat attempt gagal ke history (hist kosong = nggak dicatat).
// delay < 0 = run_after nggak diubah.
func (w *Worker) end(ctx context.Context, r *run, o outcome, status string, delay time.Duration, msg string, hist history.Status) error {
	err := w.endTx(ctx, r, o, status, delay, msg, hist)
	if err != nil && hist != "" && !errors.Is(err, errNotOwned) {
		// gagal nyatet history jangan bikin job nyangkut: status tetep diupdate
		obs.Logger(ctx).Error("history.record_failed", "status", string(hist), "error", obs.Redact(err.Error()))
		return w.endTx(ctx, r, o, status, delay, msg, "")
	}
	return err
}

func (w *Worker) endTx(ctx context.Context, r *run, o outcome, status string, delay time.Duration, msg string, hist history.Status) error {
	tx, err := w.platform.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin finalize: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := w.setStatus(ctx, tx, r, o, status, delay, msg); err != nil {
		return err
	}
	if hist != "" && r.recordable() {
		if err := w.record(ctx, tx, r.result(o, hist, msg)); err != nil {
			return fmt.Errorf("catat history: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit finalize: %w", err)
	}
	return nil
}

func (w *Worker) setStatus(ctx context.Context, tx pgx.Tx, r *run, o outcome, status string, delay time.Duration, msg string) error {
	var after *float64
	if delay >= 0 {
		s := delay.Seconds()
		after = &s
	}
	final := status == "success" || status == "failed" || status == "partial"
	var crID int64
	err := tx.QueryRow(ctx, `
		UPDATE jobs SET status = $3, attempt = $4,
			run_after = CASE WHEN $5::float8 IS NULL THEN run_after ELSE now() + make_interval(secs => $5::float8) END,
			finished_at = CASE WHEN $6::bool THEN now() ELSE NULL END,
			last_error = $7, last_sqlstate = $8, lease_until = NULL,
			batches_committed = GREATEST(batches_committed, $9),
			rows_committed = GREATEST(rows_committed, $10)
		WHERE id = $1 AND worker_id = $2 AND status = 'running'
		RETURNING change_request_id`,
		r.job.ID, w.id, status, r.attempt, after, final, nullStr(msg), nullStr(o.sqlstate), o.batches, o.rows,
	).Scan(&crID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotOwned
	}
	if err != nil {
		return fmt.Errorf("update status job: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE change_requests SET status = $2, updated_at = now() WHERE id = $1`, crID, status,
	); err != nil {
		return fmt.Errorf("update status change request: %w", err)
	}
	return nil
}

// closeChain nutup anggota keluarga eksekusi (lihat famCTE) yang masih partial. Yang arahnya
// sama dengan job ini = ikut beres (success), yang arahnya kebalik = dibatalin revert (failed).
func closeChain(ctx context.Context, tx pgx.Tx, r *run) error {
	rows, err := tx.Query(ctx, famCTE+`
		SELECT id, status, dir FROM fam WHERE id <> $1 AND status = 'partial'`, r.job.ID)
	if err != nil {
		return fmt.Errorf("baca keluarga eksekusi: %w", err)
	}
	type link struct {
		id  int64
		dir string
	}
	var open []link
	for rows.Next() {
		var l link
		var st string
		if err := rows.Scan(&l.id, &st, &l.dir); err != nil {
			rows.Close()
			return fmt.Errorf("baca keluarga eksekusi: %w", err)
		}
		open = append(open, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("baca keluarga eksekusi: %w", err)
	}

	cur := string(r.plan.Direction)
	for _, l := range open {
		st, msg := "success", ""
		if l.dir != cur {
			st, msg = "failed", fmt.Sprintf("Dibatalkan lewat revert (job #%d)", r.job.ID)
		}
		var crID int64
		err := tx.QueryRow(ctx, `
			UPDATE jobs SET status = $2, last_error = $3, last_sqlstate = NULL, finished_at = now()
			WHERE id = $1 AND status = 'partial'
			RETURNING change_request_id`, l.id, st, nullStr(msg),
		).Scan(&crID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("tutup job #%d: %w", l.id, err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE change_requests SET status = $2, updated_at = now() WHERE id = $1`, crID, st,
		); err != nil {
			return fmt.Errorf("tutup change request job #%d: %w", l.id, err)
		}
	}
	return nil
}

func flip(d history.Direction) history.Direction {
	if d == history.Up {
		return history.Down
	}
	return history.Up
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return obs.Redact(err.Error())
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
