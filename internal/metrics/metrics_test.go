package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExposition(t *testing.T) {
	r := New()
	c := r.Counter("worker_jobs_total", "Job selesai per status.", "status", "action")
	c.Inc("success", "apply")
	c.Add(2, "failed", "rollback")
	h := r.Histogram("worker_migration_seconds", "Durasi migration.", []float64{1, 5}, "mode")
	h.Observe(0.5, "normal")
	h.Observe(3, "normal")
	h.Observe(9, "normal")
	r.GaugeFunc("worker_queue_jobs", "Job per status.", func() []Sample {
		return []Sample{{Labels: []string{"queued"}, Value: 4}, {Labels: []string{"x", "kelebihan"}, Value: 1}}
	}, "status")

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	out := rec.Body.String()

	for _, want := range []string{
		"# TYPE worker_jobs_total counter",
		`worker_jobs_total{status="failed",action="rollback"} 2`,
		`worker_jobs_total{status="success",action="apply"} 1`,
		`worker_migration_seconds_bucket{mode="normal",le="1"} 1`,
		`worker_migration_seconds_bucket{mode="normal",le="5"} 2`,
		`worker_migration_seconds_bucket{mode="normal",le="+Inf"} 3`,
		`worker_migration_seconds_sum{mode="normal"} 12.5`,
		`worker_migration_seconds_count{mode="normal"} 3`,
		`worker_queue_jobs{status="queued"} 4`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output nggak ada %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "kelebihan") {
		t.Error("sample dengan jumlah label salah harusnya dibuang")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content type %q", ct)
	}
}

func TestLabelEscape(t *testing.T) {
	r := New()
	r.Counter("x_total", "x", "status").Inc(`a"b\c`)
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `x_total{status="a\"b\\c"} 1`) {
		t.Fatal(rec.Body.String())
	}
}
