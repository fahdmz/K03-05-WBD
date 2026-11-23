package obs

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type queryStartKey struct{}

type queryStart struct {
	sql    string
	params []any
	at     time.Time
}

type pgxTracer struct{}

func PgxTracer() pgx.QueryTracer {
	return pgxTracer{}
}

func (pgxTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	start := queryStart{sql: Redact(data.SQL), params: safeParams(data.Args), at: time.Now()}
	if sqlEvent(data.SQL) == "sql.result" {
		Logger(ctx).Info("sql.start", "query", start.sql, "params", start.params)
	}
	return context.WithValue(ctx, queryStartKey{}, start)
}

func (pgxTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, _ := ctx.Value(queryStartKey{}).(queryStart)
	attrs := []any{
		"query", start.sql,
		"params", start.params,
		"duration_ms", ms(time.Since(start.at)),
		"rows", data.CommandTag.RowsAffected(),
	}
	event := sqlEvent(start.sql)
	if data.Err != nil {
		var pgErr *pgconn.PgError
		if errors.As(data.Err, &pgErr) {
			attrs = append(attrs, "sqlstate", pgErr.Code)
		}
		attrs = append(attrs, "status", "error", "error", Redact(data.Err.Error()))
		Logger(ctx).Error(event, attrs...)
		return
	}
	Logger(ctx).Info(event, append(attrs, "status", "ok")...)
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
