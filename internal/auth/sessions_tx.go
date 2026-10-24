package auth

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func revokeUserSessions(ctx context.Context, db execer, now time.Time, userID int64) error {
	_, err := db.Exec(ctx, `
		UPDATE sessions SET revoked_at = $1
		WHERE user_id = $2 AND revoked_at IS NULL`,
		now, userID)
	return err
}

func rotateUserSessions(ctx context.Context, db execer, userID int64) error {
	_, err := db.Exec(ctx, `
		UPDATE sessions SET last_rotated_at = 'epoch'::timestamptz
		WHERE user_id = $1 AND revoked_at IS NULL`,
		userID)
	return err
}

type txSessionManager struct {
	tx  pgx.Tx
	now func() time.Time
}

func (s *Service) InTx(tx pgx.Tx) SessionManager {
	return txSessionManager{tx: tx, now: s.now}
}

func (m txSessionManager) RevokeUserSessions(ctx context.Context, userID int64) error {
	return revokeUserSessions(ctx, m.tx, m.now(), userID)
}

func (m txSessionManager) RotateUserSessions(ctx context.Context, userID int64) error {
	return rotateUserSessions(ctx, m.tx, userID)
}
