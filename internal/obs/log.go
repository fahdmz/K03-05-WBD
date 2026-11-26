package obs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel/trace"
)

type loggerKey struct{}

var base = slog.New(newHandler(os.Stdout))

func newHandler(w io.Writer) slog.Handler {
	return slog.NewJSONHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.MessageKey {
				a.Key = "event"
			}
			return a
		},
	})
}

func Setup(service string) (func(context.Context) error, error) {
	base = slog.New(newHandler(os.Stdout)).With("service", service)
	slog.SetDefault(base)
	return setupTracing(service)
}

func Logger(ctx context.Context) *slog.Logger {
	l := stored(ctx)
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		l = l.With("trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
	}
	return l
}

func With(ctx context.Context, args ...any) context.Context {
	return context.WithValue(ctx, loggerKey{}, stored(ctx).With(args...))
}

func stored(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return base
}

func ms(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}
