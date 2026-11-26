package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/obs"
	"github.com/fahdmz/K03-05-WBD/internal/store"
)

// recorded: eksekusi ini udah punya baris sukses di migration_history?
func recorded(ctx context.Context, q store.Querier, execID string) (bool, error) {
	var ok bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM migration_history WHERE execution_id = $1 AND status = 'success')`, execID,
	).Scan(&ok); err != nil {
		return false, fmt.Errorf("cek migration history: %w", err)
	}
	return ok, nil
}

// markUncertain: hasil di target belum pasti, nanti di-claim lagi buat dicek buktinya
func (w *Worker) markUncertain(ctx context.Context, r *run, o outcome, cause error) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	log := obs.Logger(ctx)
	delay := w.backoff(r.attempt)
	stuck := r.attempt >= r.job.MaxAttempts
	if stuck {
		// percobaan udah habis, butuh dicek manual; tetep dicoba tiap jam
		delay = time.Hour
	}
	msg := errMsg(cause)
	err := w.end(ctx, r, o, "uncertain", delay, msg, "")
	switch {
	case errors.Is(err, errNotOwned):
		log.Warn("job.lost", "outcome", "uncertain")
	case err != nil:
		// job dibiarin running, lease habis → di-claim lagi sebagai recovery
		log.Error("job.finalize_failed", "outcome", "uncertain", "error", obs.Redact(err.Error()))
		return false
	case stuck:
		log.Error("job.uncertain", "error", msg, "delay_ms", delay.Milliseconds(), "needs_manual_check", true)
	default:
		log.Warn("job.uncertain", "error", msg, "delay_ms", delay.Milliseconds())
	}
	return true
}
