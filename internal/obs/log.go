package obs

import (
	"context"
	"errors"
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
	var h slog.Handler = newHandler(os.Stdout)
	shutdownLogs := func(context.Context) error { return nil }
	if otlpEnabled() {
		otlpHandler, stop, err := setupOTLPLogs(service)
		if err != nil {
			return nil, err
		}
		h, shutdownLogs = slog.NewMultiHandler(h, otlpHandler), stop
	}
	base = slog.New(h).With("service", service)
	slog.SetDefault(base)
	shutdownTraces, err := setupTracing(service)
	if err != nil {
		shutdownLogs(context.Background())
		return nil, err
	}
	return func(ctx context.Context) error {
		return errors.Join(shutdownTraces(ctx), shutdownLogs(ctx))
	}, nil
}

func Logger(ctx context.Context) *slog.Logger {
	l := stored(ctx)
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		l = l.With("trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
	}
	return slog.New(ctxHandler{Handler: l.Handler(), ctx: ctx})
}

// ctxHandler meneruskan ctx dari Logger(ctx) ke handler di bawahnya, karena
// pemanggil memakai .Info, bukan .InfoContext. Dengan begitu bridge OTLP bisa
// mengisi TraceId dan SpanId bawaan di LogRecord. Handler JSON stdout tidak
// memakai ctx.
type ctxHandler struct {
	slog.Handler
	ctx context.Context
}

func (h ctxHandler) Enabled(_ context.Context, l slog.Level) bool {
	return h.Handler.Enabled(h.ctx, l)
}

func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		ctx = h.ctx
	}
	return h.Handler.Handle(ctx, r)
}

func (h ctxHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ctxHandler{Handler: h.Handler.WithAttrs(attrs), ctx: h.ctx}
}

func (h ctxHandler) WithGroup(name string) slog.Handler {
	return ctxHandler{Handler: h.Handler.WithGroup(name), ctx: h.ctx}
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
