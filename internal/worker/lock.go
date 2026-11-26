package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/obs"
	"github.com/fahdmz/K03-05-WBD/internal/queue"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
)

type lockRes int

const (
	lockOK lockRes = iota
	lockBusy
	lockRecovered // ngambil alih dari job lain yang masih running: job itu ditandai uncertain
)

// lock: true kalau lock kepegang. Kalau nggak, job udah dibalikin ke antrean.
func (w *Worker) lock(ctx context.Context, j job) (bool, error) {
	sctx, span := obs.Start(ctx, "lock.acquire", attribute.Int64("db.id", j.DBID))
	t0 := time.Now()
	res, other, err := w.acquire(sctx, j)
	obs.End(span, err)
	w.mx.locked(time.Since(t0), lockResult(res, err))
	log := obs.Logger(ctx)
	switch {
	case err != nil:
		log.Error("lock.failed", "error", obs.Redact(err.Error()))
		w.requeue(ctx, j, w.waitDelay)
		return false, err
	case res == lockBusy:
		log.Info("lock.busy", "status", "busy", "holder_job_id", other)
		w.requeue(ctx, j, w.waitDelay)
		return false, nil
	case res == lockRecovered:
		log.Warn("lock.recovered", "status", "uncertain", "old_job_id", other)
		w.requeue(ctx, j, 0)
		return false, nil
	}
	log.Info("lock.acquired", "status", "ok", "taken_from_job_id", other)
	return true, nil
}

func (w *Worker) acquire(ctx context.Context, j job) (lockRes, int64, error) {
	tx, err := w.platform.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("begin lock: %w", err)
	}
	defer tx.Rollback(ctx)

	var holder int64
	var expired bool
	err = tx.QueryRow(ctx, `
		SELECT job_id, lease_until < now() FROM database_locks
		WHERE db_connection_id = $1 FOR UPDATE`, j.DBID,
	).Scan(&holder, &expired)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		tag, err := tx.Exec(ctx, `
			INSERT INTO database_locks (db_connection_id, job_id, worker_id, lease_until)
			VALUES ($1, $2, $3, now() + make_interval(secs => $4))
			ON CONFLICT (db_connection_id) DO NOTHING`,
			j.DBID, j.ID, w.id, w.lease.Seconds(),
		)
		if err != nil {
			return 0, 0, fmt.Errorf("ambil lock: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return lockBusy, 0, nil
		}
	case err != nil:
		return 0, 0, fmt.Errorf("baca lock: %w", err)
	case !expired:
		// lease masih jalan (termasuk job yang sama dipegang worker lain yang masih hidup)
		return lockBusy, holder, nil
	default:
		if _, err := tx.Exec(ctx, `
			UPDATE database_locks SET job_id = $2, worker_id = $3, acquired_at = now(),
				lease_until = now() + make_interval(secs => $4)
			WHERE db_connection_id = $1`,
			j.DBID, j.ID, w.id, w.lease.Seconds(),
		); err != nil {
			return 0, 0, fmt.Errorf("ambil alih lock: %w", err)
		}
	}

	res := lockOK
	if holder != 0 && holder != j.ID {
		// pemegang lama mati. Kalau belum sempet nyentuh target (attempt 0) cukup dibalikin
		// ke antrean; kalau udah, hasilnya belum pasti → uncertain dan kita ngalah dulu.
		var crID int64
		var attempt int
		err := tx.QueryRow(ctx, `
			UPDATE jobs SET
				status = CASE WHEN attempt > 0 THEN 'uncertain' ELSE 'queued' END,
				run_after = now(), lease_until = NULL,
				last_error = CASE WHEN attempt > 0
					THEN 'Worker sebelumnya berhenti di tengah jalan, hasil perlu dicek ulang' ELSE last_error END
			WHERE id = $1 AND status = 'running'
			RETURNING change_request_id, attempt`, holder,
		).Scan(&crID, &attempt)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return 0, 0, fmt.Errorf("balikin job %d: %w", holder, err)
		default:
			st := "queued"
			if attempt > 0 {
				st = "uncertain"
			}
			if _, err := tx.Exec(ctx, `
				UPDATE change_requests SET status = $2, updated_at = now() WHERE id = $1`, crID, st,
			); err != nil {
				return 0, 0, fmt.Errorf("update change request job %d: %w", holder, err)
			}
			if attempt > 0 {
				if _, err := tx.Exec(ctx, `
					DELETE FROM database_locks WHERE db_connection_id = $1 AND job_id = $2`, j.DBID, j.ID,
				); err != nil {
					return 0, 0, fmt.Errorf("lepas lock: %w", err)
				}
				res = lockRecovered
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit lock: %w", err)
	}
	return res, holder, nil
}

func (w *Worker) release(ctx context.Context, j job) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	tag, err := w.platform.Exec(ctx, `
		DELETE FROM database_locks WHERE db_connection_id = $1 AND job_id = $2 AND worker_id = $3`,
		j.DBID, j.ID, w.id,
	)
	if err != nil {
		obs.Logger(ctx).Error("lock.release_failed", "error", obs.Redact(err.Error()))
		return
	}
	if tag.RowsAffected() > 0 {
		obs.Logger(ctx).Info("lock.released", "status", "ok")
	}
}

// extend: perpanjang lease lock + job. false = lock udah bukan punya kita.
func (w *Worker) extend(ctx context.Context, j job) (bool, error) {
	tag, err := w.platform.Exec(ctx, `
		UPDATE database_locks SET lease_until = now() + make_interval(secs => $4)
		WHERE db_connection_id = $1 AND job_id = $2 AND worker_id = $3`,
		j.DBID, j.ID, w.id, w.lease.Seconds(),
	)
	if err != nil {
		return false, fmt.Errorf("perpanjang lock: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := w.platform.Exec(ctx, `
		UPDATE jobs SET lease_until = now() + make_interval(secs => $3)
		WHERE id = $1 AND worker_id = $2 AND status = 'running'`,
		j.ID, w.id, w.lease.Seconds(),
	); err != nil {
		return true, fmt.Errorf("perpanjang lease job: %w", err)
	}
	return true, nil
}

// heartbeat jalan di goroutine sampe stop dipanggil. Lock ilang, atau lease nggak berhasil
// diperpanjang sampe mepet habis → cancel ctx job (fencing: jangan lanjut kalau udah bisa diambil alih).
func (w *Worker) heartbeat(ctx context.Context, j job, cancel context.CancelCauseFunc) func() {
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(w.beat)
		defer t.Stop()
		lastOK := time.Now()
		for {
			select {
			case <-quit:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
			ectx, ecancel := context.WithTimeout(ctx, w.beat)
			ok, err := w.extend(ectx, j)
			ecancel()
			switch {
			case err == nil && ok:
				lastOK = time.Now()
				continue
			case err == nil:
				obs.Logger(ctx).Error("lock.lost", "status", "lost")
				cancel(errLockLost)
				return
			case ctx.Err() != nil:
				return
			}
			obs.Logger(ctx).Warn("lock.heartbeat_failed", "error", obs.Redact(err.Error()))
			if time.Since(lastOK) >= w.lease-w.beat {
				obs.Logger(ctx).Error("lock.lost", "status", "lost", "reason", "heartbeat gagal terus")
				cancel(errLockLost)
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(quit)
			<-done
		})
	}
}

func (w *Worker) requeue(ctx context.Context, j job, delay time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if err := queue.Requeue(ctx, w.platform, j.ID, w.id, j.touchedUnsure(), delay); err != nil {
		obs.Logger(ctx).Error("job.requeue_failed", "error", obs.Redact(err.Error()))
		return false
	}
	return true
}

func lockResult(res lockRes, err error) string {
	switch {
	case err != nil:
		return "error"
	case res == lockBusy:
		return "busy"
	case res == lockRecovered:
		return "recovered"
	}
	return "acquired"
}
