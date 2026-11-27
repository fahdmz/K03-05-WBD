package obs

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type queryStartKey struct{}

type queryStart struct {
	sql    string
	params []any
	at     time.Time
	span   trace.Span
}

type pgxTracer struct{}

func PgxTracer() pgx.QueryTracer {
	return pgxTracer{}
}

func (pgxTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	event := sqlEvent(data.SQL)
	sql := Redact(data.SQL)
	ctx, span := Tracer().Start(ctx, spanName(event),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.statement", sql),
		),
	)
	start := queryStart{sql: sql, params: safeParams(queryArgs(data.Args)), at: time.Now(), span: span}
	if event == "sql.result" {
		Logger(ctx).Info("sql.start", "query", start.sql, "params", start.params)
	}
	return context.WithValue(ctx, queryStartKey{}, start)
}

func (pgxTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, ok := ctx.Value(queryStartKey{}).(queryStart)
	if !ok {
		return
	}
	rows := data.CommandTag.RowsAffected()
	attrs := []any{
		"query", start.sql,
		"params", start.params,
		"duration_ms", ms(time.Since(start.at)),
		"rows", rows,
	}
	start.span.SetAttributes(attribute.Int64("db.rows_affected", rows))
	event := sqlEvent(start.sql)

	if data.Err != nil {
		msg := Redact(data.Err.Error())
		var pgErr *pgconn.PgError
		if errors.As(data.Err, &pgErr) {
			attrs = append(attrs, "sqlstate", pgErr.Code)
			start.span.SetAttributes(attribute.String("db.sqlstate", pgErr.Code))
		}
		attrs = append(attrs, "status", "error", "error", msg)
		Logger(ctx).Error(event, attrs...)
		start.span.RecordError(errors.New(msg))
		start.span.SetStatus(codes.Error, msg)
		start.span.End()
		return
	}
	Logger(ctx).Info(event, append(attrs, "status", "ok")...)
	start.span.End()
}

func queryArgs(args []any) []any {
	out := make([]any, 0, len(args))
	for _, a := range args {
		switch a.(type) {
		case pgx.QueryExecMode, pgx.QueryResultFormats, pgx.QueryResultFormatsByOID:
			continue
		}
		out = append(out, a)
	}
	return out
}

func sqlEvent(sql string) string {
	s := strings.ToLower(strings.TrimSpace(sql))
	switch {
	case s == "commit":
		return "sql.commit"
	case s == "rollback":
		return "sql.rollback"
	case strings.HasPrefix(s, "begin"):
		return "sql.begin"
	default:
		return "sql.result"
	}
}

func spanName(event string) string {
	if event == "sql.result" {
		return "db.query"
	}
	return "db." + strings.TrimPrefix(event, "sql.")
}
