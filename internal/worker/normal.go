package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/queue"
	"github.com/fahdmz/K03-05-WBD/internal/sqlcheck"
	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/jackc/pgx/v5"
)

func runNormal(ctx context.Context, r *run) outcome {
	o := outcome{executedSQL: r.plan.SQL}

	// recovery / retry: udah pernah commit → jangan dijalanin ulang
	at, done, err := committedAt(ctx, r.target, r.root, r.dir())
	if err != nil {
		if r.job.Attempt > 0 && ctx.Err() == nil {
			// attempt sebelumnya mungkin udah commit, jangan dianggap retry/gagal biasa
			o.kind, o.err = okUncertain, err
			return o
		}
		return classify(ctx, err, o)
	}
	if done {
		o.kind, o.startedAt, o.committedAt = okDone, at, at
		return o
	}

	if kw, bad := sqlcheck.TxControl(r.plan.SQL); bad {
		o.kind = okFail
		o.err = fmt.Errorf("SQL tidak boleh berisi perintah transaksi (%s)", kw)
		return o
	}

	o.startedAt = time.Now()
	tx, err := r.target.Begin(ctx)
	if err != nil {
		return classify(ctx, fmt.Errorf("gagal mulai transaksi di target: %w", err), o)
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		tx.Rollback(rctx)
	}()

	// samain cara baca string sama sqlcheck, biar COMMIT nggak bisa diselundupin lewat '\''
	if _, err := tx.Exec(ctx, "SET LOCAL standard_conforming_strings = on"); err != nil {
		return classify(ctx, fmt.Errorf("gagal menyiapkan transaksi di target: %w", err), o)
	}
	tag, err := tx.Exec(ctx, r.plan.SQL, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		if r.job.Attempt > 0 && ctx.Err() == nil {
			// bisa aja eksekutor lama (worker zombie) commit duluan pas kita jalan, cek bukti lagi
			quietRollback(ctx, tx)
			if at, done, cerr := committedAt(ctx, r.target, r.root, r.dir()); cerr == nil && done {
				o.kind, o.startedAt, o.committedAt = okDone, at, at
				return o
			}
		}
		return classify(ctx, fmt.Errorf("SQL gagal dijalankan: %w", err), o)
	}
	if st := tx.Conn().PgConn().TxStatus(); st != 'T' {
		o.kind = okFail
		o.err = errors.New("SQL mengakhiri transaksi sendiri (COMMIT/ROLLBACK), hasil di target perlu dicek manual")
		return o
	}
	if err := markExecuted(ctx, tx, r.root, r.attempt, r.dir()); err != nil {
		if queue.SQLState(err) == "23505" {
			// udah ada bukti dari eksekusi lain, biar recovery yang ngecek
			o.kind, o.err = okUncertain, fmt.Errorf("bukti eksekusi sudah ada di target: %w", err)
			return o
		}
		return classify(ctx, err, o)
	}
	if err := tx.Commit(ctx); err != nil {
		o.kind, o.err = okUncertain, fmt.Errorf("COMMIT ke database target tidak pasti berhasil: %w", err)
		o.sqlstate = queue.SQLState(err)
		return o
	}
	o.kind = okDone
	o.rows = tag.RowsAffected()
	o.committedAt = time.Now()
	return o
}

func committedAt(ctx context.Context, q store.Querier, root, dir string) (time.Time, bool, error) {
	var at time.Time
	err := q.QueryRow(ctx, `
		SELECT committed_at FROM _platform.executions WHERE execution_id = $1 AND direction = $2`,
		root, dir,
	).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return at, false, nil
	}
	if err != nil {
		return at, false, fmt.Errorf("cek bukti eksekusi: %w", err)
	}
	return at, true, nil
}

// classify: ctx batal → abort, error sementara → retry, sisanya gagal permanen
func classify(ctx context.Context, err error, o outcome) outcome {
	o.err = err
	o.sqlstate = queue.SQLState(err)
	switch {
	case ctx.Err() != nil:
		o.kind = okAbort
	case queue.IsTransient(err):
		o.kind = okRetry
	default:
		o.kind = okFail
	}
	return o
}
