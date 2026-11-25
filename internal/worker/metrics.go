package worker

import (
	"context"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/metrics"
	"github.com/fahdmz/K03-05-WBD/internal/obs"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Metrics worker (bonus observability). Label cuma status/action/mode/direction/result.
type Metrics struct {
	finished  *metrics.Value
	retries   *metrics.Value
	duration  *metrics.Histogram
	queueWait *metrics.Histogram
	lockWait  *metrics.Histogram
	batches   *metrics.Value
	rows      *metrics.Value
}

func NewMetrics(reg *metrics.Registry, platform *pgxpool.Pool) *Metrics {
	m := &Metrics{
		finished: reg.Counter("worker_jobs_finished_total",
			"Job yang selesai diproses worker per status akhir.", "status", "action", "mode"),
		retries: reg.Counter("worker_job_retries_total",
			"Retry yang dijadwalkan karena error sementara.", "action", "mode"),
		duration: reg.Histogram("worker_migration_duration_seconds",
			"Durasi proses job dari claim sampai selesai.", metrics.DurationBuckets, "status", "action", "mode"),
		queueWait: reg.Histogram("worker_queue_wait_seconds",
			"Lama job nunggu di antrean sebelum di-claim.", metrics.DurationBuckets),
		lockWait: reg.Histogram("worker_lock_wait_seconds",
			"Lama percobaan ngambil lock database target.", metrics.DurationBuckets, "result"),
		batches: reg.Counter("worker_batches_committed_total",
			"Batch yang commit di database target.", "direction"),
		rows: reg.Counter("worker_rows_committed_total",
			"Row yang diproses batch yang commit.", "direction"),
	}

	reg.GaugeFunc("worker_jobs", "Jumlah job per status di antrean.", func() []metrics.Sample {
		return jobCounts(platform)
	}, "status")
	reg.GaugeFunc("worker_db_pool_connections", "Koneksi pool database platform di worker.", func() []metrics.Sample {
		s := platform.Stat()
		return []metrics.Sample{
			{Labels: []string{"active"}, Value: float64(s.AcquiredConns())},
			{Labels: []string{"idle"}, Value: float64(s.IdleConns())},
			{Labels: []string{"max"}, Value: float64(s.MaxConns())},
		}
	}, "state")
	reg.CounterFunc("worker_db_pool_acquire_wait_seconds_total",
		"Total waktu nunggu dapet koneksi dari pool platform.", func() []metrics.Sample {
			return []metrics.Sample{{Value: platform.Stat().AcquireDuration().Seconds()}}
		})
	return m
}

var jobStatuses = []string{"queued", "running", "success", "failed", "partial", "uncertain"}

func jobCounts(db *pgxpool.Pool) []metrics.Sample {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got := map[string]float64{}
	rows, err := db.Query(ctx, `SELECT status, count(*) FROM jobs GROUP BY status`)
	if err != nil {
		obs.Logger(ctx).Warn("metrics.query_failed", "error", obs.Redact(err.Error()))
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int64
		if rows.Scan(&st, &n) == nil {
			got[st] = float64(n)
		}
	}
	out := make([]metrics.Sample, 0, len(jobStatuses))
	for _, st := range jobStatuses {
		out = append(out, metrics.Sample{Labels: []string{st}, Value: got[st]})
	}
	return out
}

func (w *Worker) SetMetrics(m *Metrics) {
	w.mx = m
}

func labels(r *run) (string, string) {
	if r == nil || r.plan.Action == "" {
		return "unknown", "unknown"
	}
	mode := r.plan.Mode
	if mode == "" {
		mode = "normal"
	}
	return string(r.plan.Action), mode
}

func (m *Metrics) jobDone(r *run, status string, began time.Time) {
	if m == nil {
		return
	}
	a, md := labels(r)
	m.finished.Inc(status, a, md)
	m.duration.Observe(time.Since(began).Seconds(), status, a, md)
}

func (m *Metrics) retry(r *run) {
	if m == nil {
		return
	}
	a, md := labels(r)
	m.retries.Inc(a, md)
}

func (m *Metrics) waited(sec float64) {
	if m != nil && sec >= 0 {
		m.queueWait.Observe(sec)
	}
}

func (m *Metrics) locked(d time.Duration, result string) {
	if m != nil {
		m.lockWait.Observe(d.Seconds(), result)
	}
}

func (m *Metrics) batch(dir string, n int, rows int64) {
	if m == nil || n <= 0 {
		return
	}
	m.batches.Add(float64(n), dir)
	m.rows.Add(float64(rows), dir)
}
