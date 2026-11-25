package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func useTestTracer(t *testing.T) {
	t.Helper()
	provider := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { provider.Shutdown(context.Background()) })
}

func TestTracePropagation(t *testing.T) {
	useTestTracer(t)
	ctx, span := Start(context.Background(), "test.parent")
	defer span.End()

	carrier := InjectTrace(ctx)
	if carrier["traceparent"] == "" {
		t.Fatalf("traceparent kosong: %v", carrier)
	}
	restored := trace.SpanContextFromContext(ExtractTrace(context.Background(), carrier))
	if restored.TraceID() != span.SpanContext().TraceID() {
		t.Fatalf("trace id = %s, want %s", restored.TraceID(), span.SpanContext().TraceID())
	}
}

func TestLoggerIncludesTraceID(t *testing.T) {
	useTestTracer(t)
	var buf bytes.Buffer
	old := base
	base = slog.New(slog.NewJSONHandler(&buf, nil))
	t.Cleanup(func() { base = old })

	ctx, span := Start(context.Background(), "test.log")
	defer span.End()
	Logger(With(ctx, "actor", "tester")).Info("test.event")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log bukan JSON: %v: %s", err, buf.String())
	}
	if line["trace_id"] != span.SpanContext().TraceID().String() {
		t.Fatalf("trace_id = %v, want %s", line["trace_id"], span.SpanContext().TraceID())
	}
	if line["actor"] != "tester" {
		t.Fatalf("actor = %v, want tester", line["actor"])
	}
	if n := strings.Count(buf.String(), "trace_id"); n != 1 {
		t.Fatalf("trace_id muncul %d kali, want 1", n)
	}
}

func TestSpanName(t *testing.T) {
	cases := map[string]string{
		"sql.result":   "db.query",
		"sql.begin":    "db.begin",
		"sql.commit":   "db.commit",
		"sql.rollback": "db.rollback",
	}
	for in, want := range cases {
		if got := spanName(in); got != want {
			t.Errorf("spanName(%q) = %q, want %q", in, got, want)
		}
	}
}
