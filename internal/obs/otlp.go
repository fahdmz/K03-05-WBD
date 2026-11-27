package obs

import (
	"context"
	"log/slog"
	"os"
	"slices"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// OTLP aktif kalau OTEL_EXPORTER_OTLP_ENDPOINT di-set, misalnya http://localhost:4318.
// Exporter membaca endpoint dari env, menambahkan /v1/traces atau /v1/logs,
// dan memakai koneksi insecure untuk skema http.
func otlpEnabled() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != ""
}

func setupOTLPLogs(service string) (slog.Handler, func(context.Context) error, error) {
	exporter, err := otlploghttp.New(context.Background())
	if err != nil {
		return nil, nil, err
	}
	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
		sdklog.WithResource(serviceResource(service)),
	)
	h := otelslog.NewHandler(tracerName, otelslog.WithLoggerProvider(provider))
	return withoutTraceAttrs{h}, provider.Shutdown, nil
}

// Di OTLP, trace context sudah dibawa TraceId dan SpanId bawaan LogRecord, jadi
// atribut trace_id dan span_id tidak dikirim lagi supaya tidak dobel di structured
// metadata Loki. Stdout tidak terpengaruh.
type withoutTraceAttrs struct{ slog.Handler }

func (h withoutTraceAttrs) WithAttrs(attrs []slog.Attr) slog.Handler {
	return withoutTraceAttrs{h.Handler.WithAttrs(slices.DeleteFunc(slices.Clone(attrs), isTraceAttr))}
}

func (h withoutTraceAttrs) WithGroup(name string) slog.Handler {
	return withoutTraceAttrs{h.Handler.WithGroup(name)}
}

func isTraceAttr(a slog.Attr) bool {
	return a.Key == "trace_id" || a.Key == "span_id"
}
