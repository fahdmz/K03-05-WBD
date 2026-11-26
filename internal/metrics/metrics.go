package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Exporter format teks Prometheus versi mini, sengaja tanpa dependency baru.
// Label cuma boleh yang nilainya terbatas (status, action, mode), jangan id/SQL.

type Registry struct {
	mu   sync.Mutex
	list []collector
}

type collector interface {
	write(w io.Writer)
}

func New() *Registry {
	return &Registry{}
}

func (r *Registry) add(c collector) {
	r.mu.Lock()
	r.list = append(r.list, c)
	r.mu.Unlock()
}

func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.mu.Lock()
		list := append([]collector(nil), r.list...)
		r.mu.Unlock()
		for _, c := range list {
			c.write(w)
		}
	})
}

type vec struct {
	mu     sync.Mutex
	name   string
	help   string
	kind   string
	labels []string
	keys   map[string][]string
}

func (v *vec) key(lv []string) string {
	if len(lv) != len(v.labels) {
		panic(fmt.Sprintf("metrics %s: butuh %d label, dapet %d", v.name, len(v.labels), len(lv)))
	}
	k := strings.Join(lv, "\xff")
	if _, ok := v.keys[k]; !ok {
		v.keys[k] = append([]string(nil), lv...)
	}
	return k
}

func (v *vec) sorted() []string {
	ks := make([]string, 0, len(v.keys))
	for k := range v.keys {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func (v *vec) header(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", v.name, v.help, v.name, v.kind)
}

func labelStr(names, vals []string, extra ...string) string {
	parts := make([]string, 0, len(names)+1)
	for i, n := range names {
		parts = append(parts, n+`="`+esc(vals[i])+`"`)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		parts = append(parts, extra[i]+`="`+esc(extra[i+1])+`"`)
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ",") + "}"
}

var escaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)

func esc(s string) string { return escaper.Replace(s) }

// FormatFloat udah nulis +Inf sendiri
func num(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// Counter

type Value struct {
	vec
	vals map[string]float64
}

func (r *Registry) Counter(name, help string, labels ...string) *Value {
	v := &Value{vec: vec{name: name, help: help, kind: "counter", labels: labels, keys: map[string][]string{}}, vals: map[string]float64{}}
	r.add(v)
	return v
}

func (v *Value) Add(d float64, lv ...string) {
	v.mu.Lock()
	v.vals[v.key(lv)] += d
	v.mu.Unlock()
}

func (v *Value) Inc(lv ...string) { v.Add(1, lv...) }

func (v *Value) write(w io.Writer) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.header(w)
	for _, k := range v.sorted() {
		fmt.Fprintf(w, "%s%s %s\n", v.name, labelStr(v.labels, v.keys[k]), num(v.vals[k]))
	}
}

// Histogram

type Histogram struct {
	vec
	buckets []float64
	counts  map[string][]uint64
	sums    map[string]float64
	totals  map[string]uint64
}

func (r *Registry) Histogram(name, help string, buckets []float64, labels ...string) *Histogram {
	b := append([]float64(nil), buckets...)
	sort.Float64s(b)
	h := &Histogram{
		vec:     vec{name: name, help: help, kind: "histogram", labels: labels, keys: map[string][]string{}},
		buckets: b,
		counts:  map[string][]uint64{},
		sums:    map[string]float64{},
		totals:  map[string]uint64{},
	}
	r.add(h)
	return h
}

func (h *Histogram) Observe(x float64, lv ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := h.key(lv)
	c := h.counts[k]
	if c == nil {
		c = make([]uint64, len(h.buckets))
		h.counts[k] = c
	}
	for i, b := range h.buckets {
		if x <= b {
			c[i]++
		}
	}
	h.sums[k] += x
	h.totals[k]++
}

func (h *Histogram) write(w io.Writer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.header(w)
	for _, k := range h.sorted() {
		lv := h.keys[k]
		for i, b := range h.buckets {
			fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, labelStr(h.labels, lv, "le", num(b)), h.counts[k][i])
		}
		fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, labelStr(h.labels, lv, "le", "+Inf"), h.totals[k])
		fmt.Fprintf(w, "%s_sum%s %s\n", h.name, labelStr(h.labels, lv), num(h.sums[k]))
		fmt.Fprintf(w, "%s_count%s %d\n", h.name, labelStr(h.labels, lv), h.totals[k])
	}
}

// GaugeFunc: nilainya diambil pas di-scrape (mis. jumlah job per status dari DB)

type Sample struct {
	Labels []string
	Value  float64
}

type gaugeFunc struct {
	vec
	fn func() []Sample
}

func (r *Registry) GaugeFunc(name, help string, fn func() []Sample, labels ...string) {
	r.add(&gaugeFunc{vec: vec{name: name, help: help, kind: "gauge", labels: labels}, fn: fn})
}

// CounterFunc: kayak GaugeFunc tapi nilainya cuma naik (mis. total waktu nunggu pool)
func (r *Registry) CounterFunc(name, help string, fn func() []Sample, labels ...string) {
	r.add(&gaugeFunc{vec: vec{name: name, help: help, kind: "counter", labels: labels}, fn: fn})
}

func (g *gaugeFunc) write(w io.Writer) {
	samples := g.fn()
	g.header(w)
	for _, s := range samples {
		if len(s.Labels) != len(g.labels) {
			continue
		}
		fmt.Fprintf(w, "%s%s %s\n", g.name, labelStr(g.labels, s.Labels), num(s.Value))
	}
}

var DurationBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 900}
