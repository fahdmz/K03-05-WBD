package obs

import (
	"context"
	"log/slog"
	"os"
	"time"
)

type loggerKey struct{}

var base = slog.New(slog.NewJSONHandler(os.Stdout, nil))

func Setup(service string) (func(context.Context) error, error) {
	base = slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service)
	slog.SetDefault(base)
	return func(context.Context) error { return nil }, nil
}

func Logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return base
}

func With(ctx context.Context, args ...any) context.Context {
	return context.WithValue(ctx, loggerKey{}, Logger(ctx).With(args...))
}

func ms(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}
