package worker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/history"
	"github.com/fahdmz/K03-05-WBD/internal/obs"
	"github.com/fahdmz/K03-05-WBD/internal/queue"
	"github.com/fahdmz/K03-05-WBD/internal/sqlcheck"
	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
)

var errRevertedUp = errors.New("Batch ini sudah pernah di-revert sebagian, lanjutkan revert")

// bRange = satu batch: lo eksklusif, hi inklusif
type bRange struct {
	no     int
	lo, hi int64
}

type batcher struct {
	r       *run
	dir     string
	sql     string // udah di-bind ($1/$2)
	batches int    // total committed root+arah
	rows    int64
	out     outcome
}

func runBatch(ctx context.Context, r *run) outcome {
	b := &batcher{r: r, dir: r.dir()}
	b.out = outcome{executedSQL: r.plan.SQL, startedAt: time.Now()}

	if err := checkBatchSQL(r.plan); err != nil {
		return b.end(okFail, err)
	}
	b.sql = sqlcheck.BindBatch(r.plan.SQL)

	done, err := executed(ctx, r.target, r.root, b.dir)
	if err != nil {
		return b.stop(ctx, err)
	}
	if err := b.loadTotals(ctx); err != nil {
		return b.stop(ctx, err)
	}
	if done {
		if err := r.target.QueryRow(ctx, `
			SELECT committed_at FROM _platform.executions WHERE execution_id = $1 AND direction = $2`,
			r.root, b.dir,
		).Scan(&b.out.committedAt); err != nil {
			return b.stop(ctx, fmt.Errorf("baca bukti eksekusi: %w", err))
		}
		return b.end(okDone, nil)
	}

	// arah "asli" root (up buat apply/forward, down buat rollback) tapi udah ada jejak arah sebaliknya
	if b.dir == string(naturalDir(r.rootAction)) {
		n, err := countCkpt(ctx, r.target, r.root, string(flip(history.Direction(b.dir))))
		if err != nil {
			return b.stop(ctx, err)
		}
		if n > 0 {
			return b.end(okFail, errRevertedUp)
		}
	}

	if b.dir == string(history.Up) && r.rootAction != history.Rollback {
		return b.runCursor(ctx)
	}
	todo, err := b.pending(ctx)
	if err != nil {
		return b.stop(ctx, err)
	}
	return b.runList(ctx, todo)
}

func checkBatchSQL(p history.Plan) error {
	if p.Batch == nil || p.Batch.Table == "" || p.Batch.Column == "" || p.Batch.Size <= 0 {
		return errors.New("konfigurasi batch (tabel, kolom, ukuran) belum lengkap")
	}
	if kw, ok := sqlcheck.TxControl(p.SQL); ok {
		return fmt.Errorf("SQL batch tidak boleh mengandung perintah transaksi (%s)", kw)
	}
	if len(sqlcheck.Statements(p.SQL)) != 1 {
		return errors.New("SQL batch harus tepat satu statement")
	}
	if !sqlcheck.HasBatchTags(p.SQL) {
		return fmt.Errorf("SQL batch wajib memakai %s dan %s", sqlcheck.LoTag, sqlcheck.HiTag)
	}
	return nil
}

// runCursor: arah up dari root apply/forward, cursor jalan di atas checkpoint terakhir
func (b *batcher) runCursor(ctx context.Context) outcome {
	r := b.r
	top, err := b.scope(ctx)
	if err != nil {
		return b.stop(ctx, err)
	}
	if top == nil {
		return b.finishEmpty(ctx)
	}

	lo, no := int64(math.MinInt64), 0
	fresh := true
	err = r.target.QueryRow(ctx, `
		SELECT hi, batch_no FROM _platform.batch_checkpoints
		WHERE execution_id = $1 AND direction = $2
		ORDER BY batch_no DESC LIMIT 1`, r.root, b.dir,
	).Scan(&lo, &no)
	if err == nil {
		fresh = false
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return b.stop(ctx, fmt.Errorf("baca checkpoint terakhir: %w", err))
	}

	tbl, col := batchIdents(r.plan.Batch)
	next := `SELECT min(c)::bigint, max(c)::bigint FROM (
		SELECT ` + col + ` AS c FROM ` + tbl + `
		WHERE ` + col + ` > $1::bigint AND ` + col + ` <= $2::bigint
		ORDER BY ` + col + ` LIMIT $3) s`

	for {
		if ctx.Err() != nil {
			return b.stop(ctx, ctx.Err())
		}
		tx, err := r.target.Begin(ctx)
		if err != nil {
			return b.stop(ctx, fmt.Errorf("mulai transaksi batch: %w", err))
		}
		var first, hi *int64
		if err := tx.QueryRow(ctx, next, lo, *top, r.plan.Batch.Size).Scan(&first, &hi); err != nil {
			quietRollback(ctx, tx)
			return b.stop(ctx, fmt.Errorf("cari batas batch: %w", err))
		}
		if hi == nil {
			// udah nggak ada sisa di cakupan
			return b.commitMark(ctx, tx)
		}
		// lo awal = nilai terkecil - 1, biar aman buat kolom int4 (MinInt64 nggak muat)
		if fresh {
			lo = *first - 1
		}
		fresh = false
		cur := bRange{no: no + 1, lo: lo, hi: *hi}
		if o, ok := b.commitBatch(ctx, tx, cur, *hi == *top); !ok {
			return o
		}
		if *hi == *top {
			return b.end(okDone, nil)
		}
		lo, no = *hi, cur.no
	}
}

func (b *batcher) runList(ctx context.Context, todo []bRange) outcome {
	if len(todo) == 0 {
		return b.finishEmpty(ctx)
	}
	for i, cur := range todo {
		if ctx.Err() != nil {
			return b.stop(ctx, ctx.Err())
		}
		tx, err := b.r.target.Begin(ctx)
		if err != nil {
			return b.stop(ctx, fmt.Errorf("mulai transaksi batch: %w", err))
		}
		if o, ok := b.commitBatch(ctx, tx, cur, i == len(todo)-1); !ok {
			return o
		}
	}
	return b.end(okDone, nil)
}

// commitBatch: Exec + checkpoint (+ bukti kalau terakhir) dalam tx, lalu COMMIT
func (b *batcher) commitBatch(ctx context.Context, tx pgx.Tx, cur bRange, last bool) (outcome, bool) {
	r := b.r
	sctx, span := obs.Start(obs.With(ctx, "batch", cur.no), "batch",
		attribute.Int("batch", cur.no), attribute.Int64("lo", cur.lo), attribute.Int64("hi", cur.hi))

	tag, err := tx.Exec(sctx, b.sql, cur.lo, cur.hi)
	if err == nil {
		_, err = tx.Exec(sctx, `
			INSERT INTO _platform.batch_checkpoints (execution_id, direction, batch_no, lo, hi, rows_affected)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			r.root, b.dir, cur.no, cur.lo, cur.hi, tag.RowsAffected())
		if err != nil {
			err = &evidenceErr{fmt.Errorf("catat checkpoint batch %d: %w", cur.no, err)}
		}
	} else {
		err = fmt.Errorf("batch %d gagal: %w", cur.no, err)
	}
	if err == nil && last {
		err = markExecuted(sctx, tx, r.root, r.attempt, b.dir)
	}
	if err != nil {
		quietRollback(ctx, tx)
		obs.End(span, err)
		return b.stop(ctx, err), false
	}
	if err := tx.Commit(sctx); err != nil {
		obs.End(span, err)
		return b.end(okUncertain, fmt.Errorf("commit batch %d: %w", cur.no, err)), false
	}
	obs.End(span, nil)

	b.batches++
	b.rows += tag.RowsAffected()
	b.out.committedAt = time.Now()
	if r.progress != nil {
		r.progress(ctx, b.batches, b.rows)
	}
	if r.onBatch != nil {
		r.onBatch(tag.RowsAffected())
	}
	obs.Logger(ctx).Info("batch.committed", "status", "committed",
		"batch", cur.no, "lo", cur.lo, "hi", cur.hi, "rows", tag.RowsAffected())
	return outcome{}, true
}

// finishEmpty: nggak ada batch tersisa, cukup catat bukti
func (b *batcher) finishEmpty(ctx context.Context) outcome {
	tx, err := b.r.target.Begin(ctx)
	if err != nil {
		return b.stop(ctx, fmt.Errorf("mulai transaksi batch: %w", err))
	}
	return b.commitMark(ctx, tx)
}

func (b *batcher) commitMark(ctx context.Context, tx pgx.Tx) outcome {
	if err := markExecuted(ctx, tx, b.r.root, b.r.attempt, b.dir); err != nil {
		quietRollback(ctx, tx)
		return b.stop(ctx, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return b.end(okUncertain, fmt.Errorf("commit bukti eksekusi: %w", err))
	}
	b.out.committedAt = time.Now()
	return b.end(okDone, nil)
}

// scope: batas atas cakupan (R,'up'), dihitung & disimpen sekali
func (b *batcher) scope(ctx context.Context) (*int64, error) {
	r := b.r
	tbl, col := batchIdents(r.plan.Batch)
	tx, err := r.target.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("mulai transaksi cakupan: %w", err)
	}
	defer quietRollback(ctx, tx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO _platform.batch_scopes (execution_id, direction, max_value)
		SELECT $1, $2, (SELECT max(`+col+`)::bigint FROM `+tbl+`)
		ON CONFLICT (execution_id, direction) DO NOTHING`, r.root, b.dir,
	); err != nil {
		return nil, fmt.Errorf("hitung cakupan batch: %w", err)
	}
	var top *int64
	if err := tx.QueryRow(ctx, `
		SELECT max_value FROM _platform.batch_scopes WHERE execution_id = $1 AND direction = $2`,
		r.root, b.dir,
	).Scan(&top); err != nil {
		return nil, fmt.Errorf("baca cakupan batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("simpan cakupan batch: %w", err)
	}
	return top, nil
}

// pending: batch dari checkpoint sumber (urutan terbalik) yang belum dikerjain di arah ini
func (b *batcher) pending(ctx context.Context) ([]bRange, error) {
	r := b.r
	src, srcDir, order := r.root, string(flip(history.Direction(b.dir))), "DESC"
	if r.rootAction == history.Rollback {
		if b.dir == string(history.Down) {
			if r.rootTarget == nil {
				return nil, errors.New("eksekusi yang di-rollback tidak diketahui")
			}
			src = *r.rootTarget
			ok, err := executed(ctx, r.target, src, string(history.Up))
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, errors.New("bukti eksekusi batch yang di-rollback tidak ditemukan di database target")
			}
		} else {
			// down rollback jalan dari batch_no gede ke kecil, dibalik lagi jadi naik
			order = "ASC"
		}
	}
	rows, err := r.target.Query(ctx, `
		SELECT s.batch_no, s.lo, s.hi FROM _platform.batch_checkpoints s
		WHERE s.execution_id = $1 AND s.direction = $2
		  AND NOT EXISTS (
		      SELECT 1 FROM _platform.batch_checkpoints d
		      WHERE d.execution_id = $3 AND d.direction = $4 AND d.batch_no = s.batch_no
		  )
		ORDER BY s.batch_no `+order, src, srcDir, r.root, b.dir)
	if err != nil {
		return nil, fmt.Errorf("baca checkpoint sumber: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (bRange, error) {
		var x bRange
		err := row.Scan(&x.no, &x.lo, &x.hi)
		return x, err
	})
	if err != nil {
		return nil, fmt.Errorf("baca checkpoint sumber: %w", err)
	}
	return out, nil
}

func (b *batcher) loadTotals(ctx context.Context) error {
	if err := b.r.target.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(rows_affected), 0)::bigint FROM _platform.batch_checkpoints
		WHERE execution_id = $1 AND direction = $2`, b.r.root, b.dir,
	).Scan(&b.batches, &b.rows); err != nil {
		return fmt.Errorf("baca total checkpoint: %w", err)
	}
	return nil
}

// stop: klasifikasi error (abort / retry / partial / failed)
func (b *batcher) stop(ctx context.Context, err error) outcome {
	switch {
	case ctx.Err() != nil:
		return b.end(okAbort, err)
	case isEvidenceErr(err) && queue.SQLState(err) == "23505":
		// checkpoint/bukti udah ada dari eksekutor lain, biar recovery yang ngecek
		return b.end(okUncertain, err)
	case queue.IsTransient(err):
		return b.end(okRetry, err)
	case b.batches > 0:
		return b.end(okPartial, err)
	}
	return b.end(okFail, err)
}

func (b *batcher) end(k outcomeKind, err error) outcome {
	o := b.out
	o.kind, o.err = k, err
	o.sqlstate = queue.SQLState(err)
	o.batches, o.rows = b.batches, b.rows
	return o
}

func countCkpt(ctx context.Context, q store.Querier, root, dir string) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT count(*) FROM _platform.batch_checkpoints WHERE execution_id = $1 AND direction = $2`,
		root, dir,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("baca checkpoint: %w", err)
	}
	return n, nil
}

func batchIdents(bt *history.Batch) (tbl, col string) {
	return pgx.Identifier(strings.Split(bt.Table, ".")).Sanitize(), pgx.Identifier{bt.Column}.Sanitize()
}

func quietRollback(ctx context.Context, tx pgx.Tx) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx.Rollback(c)
}
