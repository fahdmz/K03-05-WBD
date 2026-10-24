package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestGroupAccessSessionChangesIntegration(t *testing.T) {
	tests := []struct {
		name    string
		before  string
		body    string
		after   string
		revoked bool
		rotated bool
	}{
		{
			name:    "new_access_rotates",
			body:    `{"access":[{"databaseId":10,"level":"view"}]}`,
			after:   "view",
			rotated: true,
		},
		{
			name:    "upgrade_rotates",
			before:  "view",
			body:    `{"access":[{"databaseId":10,"level":"edit"}]}`,
			after:   "edit",
			rotated: true,
		},
		{
			name:    "downgrade_revokes",
			before:  "edit",
			body:    `{"access":[{"databaseId":10,"level":"view"}]}`,
			after:   "view",
			revoked: true,
		},
		{
			name:    "removal_revokes",
			before:  "view",
			body:    `{"access":[]}`,
			revoked: true,
		},
		{
			name:   "unchanged_preserves_session",
			before: "view",
			body:   `{"access":[{"databaseId":10,"level":"view"}]}`,
			after:  "view",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAdminIntegrationEnv(t)
			e.seed(t)
			e.exec(t, "INSERT INTO group_members VALUES (10, 20)")

			if tt.before != "" {
				e.exec(t, `
					INSERT INTO group_db_access
						(group_id, db_connection_id, level)
					VALUES (10, 10, $1)
				`, tt.before)
			}

			e.session(t, 20, "access-member-session")
			e.session(t, 30, "access-outsider-session")

			e.call(t, e.res.SetGroupAccess, "PUT", "10", tt.body, 200)

			if tt.after == "" {
				e.count(t, `
					SELECT COUNT(*) FROM group_db_access
					WHERE group_id = 10
				`, 0)
			} else {
				e.count(t, `
					SELECT COUNT(*) FROM group_db_access
					WHERE group_id = 10
					  AND db_connection_id = 10
					  AND level = $1
				`, 1, tt.after)
			}

			e.count(t, `
				SELECT COUNT(*) FROM sessions
				WHERE user_id = 20
				  AND (revoked_at IS NOT NULL) = $1
				  AND (last_rotated_at = 'epoch'::timestamptz) = $2
			`, 1, tt.revoked, tt.rotated)

			e.count(t, `
				SELECT COUNT(*) FROM sessions
				WHERE user_id = 30
				  AND revoked_at IS NULL
				  AND last_rotated_at = created_at
			`, 1)
		})
	}
}

type failingAccessSessions struct {
	delegate SessionManager
}

func (s *failingAccessSessions) RevokeUserSessions(
	ctx context.Context,
	userID int64,
) error {
	if err := s.delegate.RevokeUserSessions(ctx, userID); err != nil {
		return err
	}
	return errors.New("forced failure after session revocation")
}

func (s *failingAccessSessions) RotateUserSessions(
	ctx context.Context,
	userID int64,
) error {
	if err := s.delegate.RotateUserSessions(ctx, userID); err != nil {
		return err
	}
	return errors.New("forced failure after session rotation")
}

func TestGroupAccessSessionFailureRollsBackIntegration(t *testing.T) {
	tests := []struct {
		name   string
		before string
		body   string
	}{
		{
			name:   "rotation_failure",
			before: "view",
			body:   `{"access":[{"databaseId":10,"level":"edit"}]}`,
		},
		{
			name:   "revocation_failure",
			before: "edit",
			body:   `{"access":[{"databaseId":10,"level":"view"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAdminIntegrationEnv(t)
			e.seed(t)
			e.exec(t, "INSERT INTO group_members VALUES (10, 20)")
			e.exec(t, `
				INSERT INTO group_db_access
					(group_id, db_connection_id, level)
				VALUES (10, 10, $1)
			`, tt.before)
			e.session(t, 20, "access-rollback-session")

			e.res = e.res.WithSessionManagerFactory(func(tx pgx.Tx) SessionManager {
				return &failingAccessSessions{
					delegate: &transactionTestSessions{tx: tx},
				}
			})

			e.call(t, e.res.SetGroupAccess, "PUT", "10", tt.body, 500)

			e.count(t, `
				SELECT COUNT(*) FROM group_db_access
				WHERE group_id = 10
				  AND db_connection_id = 10
				  AND level = $1
			`, 1, tt.before)

			e.count(t, `
				SELECT COUNT(*) FROM sessions
				WHERE user_id = 20
				  AND revoked_at IS NULL
				  AND last_rotated_at = created_at
			`, 1)
		})
	}
}

func TestGroupAccessRequiresSessionManagerIntegration(t *testing.T) {
	e := newAdminIntegrationEnv(t)
	e.seed(t)
	e.exec(t, "INSERT INTO group_members VALUES (10, 20)")
	e.exec(t, `
		INSERT INTO group_db_access
			(group_id, db_connection_id, level)
		VALUES (10, 10, 'view')
	`)
	e.session(t, 20, "access-missing-manager-session")
	e.res = e.res.WithSessionManagerFactory(nil)

	e.call(t, e.res.SetGroupAccess, "PUT", "10",
		`{"access":[{"databaseId":10,"level":"edit"}]}`, 503)

	e.count(t, `
		SELECT COUNT(*) FROM group_db_access
		WHERE group_id = 10 AND db_connection_id = 10 AND level = 'view'
	`, 1)

	e.count(t, `
		SELECT COUNT(*) FROM sessions
		WHERE user_id = 20
		  AND revoked_at IS NULL
		  AND last_rotated_at = created_at
	`, 1)

	e.call(t, e.res.SetGroupAccess, "PUT", "10",
		`{"access":[{"databaseId":10,"level":"view"}]}`, 200)
}

func TestGroupAccessWithoutMembersIntegration(t *testing.T) {
	e := newAdminIntegrationEnv(t)
	e.seed(t)
	e.res = e.res.WithSessionManagerFactory(nil)

	e.call(t, e.res.SetGroupAccess, "PUT", "10",
		`{"access":[{"databaseId":10,"level":"edit"}]}`, 200)

	e.count(t, `
		SELECT COUNT(*) FROM group_db_access
		WHERE group_id = 10 AND db_connection_id = 10 AND level = 'edit'
	`, 1)
}
