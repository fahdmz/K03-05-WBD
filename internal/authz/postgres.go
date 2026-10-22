package authz

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type RowQuerier interface {
	QueryRow(
		ctx context.Context,
		sql string,
		args ...any,
	) pgx.Row
}

type PostgresAuthorizer struct {
	db RowQuerier
}

var _ Authorizer = (*PostgresAuthorizer)(nil)

func NewPostgresAuthorizer(db RowQuerier) *PostgresAuthorizer {
	return &PostgresAuthorizer{db: db}
}

func (a *PostgresAuthorizer) EffectiveLevel(
	ctx context.Context,
	userID, dbID int64,
) (Level, error) {
	const query = `
		SELECT COALESCE(MAX(
			CASE gda.level
				WHEN 'edit' THEN 2
				WHEN 'view' THEN 1
				ELSE 0
			END
		), 0)
		FROM users AS u
		JOIN group_members AS gm
			ON gm.user_id = u.id
		JOIN group_db_access AS gda
			ON gda.group_id = gm.group_id
		WHERE u.id = $1
			AND u.is_active = TRUE
			AND gda.db_connection_id = $2
	`
	var level int
	err := a.db.QueryRow(ctx, query, userID, dbID).Scan(&level)
	if err != nil {
		return None, fmt.Errorf("get effective access level: %w", err)
	}
	return Level(level), nil
}

func (a *PostgresAuthorizer) Require(
	ctx context.Context,
	userID, dbID int64,
	min Level,
) error {
	if min != View && min != Edit {
		return fmt.Errorf("invalid required access level: %d", min)
	}
	level, err := a.EffectiveLevel(ctx, userID, dbID)
	if err != nil {
		return err
	}
	if level < min {
		return ErrForbidden
	}
	return nil
}