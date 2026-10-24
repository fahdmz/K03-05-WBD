package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestUserSessionChangesIntegration(t *testing.T) {
	tests := []struct {
		name         string
		beforeAdmin  bool
		beforeActive bool
		body         string
		afterAdmin   bool
		afterActive  bool
		revoked      bool
		rotated      bool
	}{
		{
			name:         "promotion_rotates",
			beforeActive: true,
			body:         `{"isAdmin":true}`,
			afterAdmin:   true,
			afterActive:  true,
			rotated:      true,
		},
		{
			name:         "demotion_revokes",
			beforeAdmin:  true,
			beforeActive: true,
			body:         `{"isAdmin":false}`,
			afterActive:  true,
			revoked:      true,
		},
		{
			name:         "deactivation_revokes",
			beforeActive: true,
			body:         `{"isActive":false}`,
			revoked:      true,
		},
		{
			name:         "password_change_revokes",
			beforeActive: true,
			body:         `{"password":"replacement123"}`,
			afterActive:  true,
			revoked:      true,
		},
		{
			name:         "unchanged_flags_preserve_session",
			beforeActive: true,
			body:         `{"isAdmin":false,"isActive":true}`,
			afterActive:  true,
		},
		{
			name:         "username_change_preserves_session",
			beforeActive: true,
			body:         `{"username":"renamed-member"}`,
			afterActive:  true,
		},
		{
			name:         "revocation_wins_over_promotion",
			beforeActive: true,
			body:         `{"isAdmin":true,"password":"replacement123"}`,
			afterAdmin:   true,
			afterActive:  true,
			revoked:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAdminIntegrationEnv(t)
			e.seed(t)
			e.exec(t, `
				UPDATE users SET is_admin=$1, is_active=$2 WHERE id=20
			`, tt.beforeAdmin, tt.beforeActive)
			e.session(t, 20, "changed-user-session")
			e.session(t, 30, "unrelated-user-session")

			e.call(t, e.users.UpdateUser, "PATCH", "20", tt.body, 200)

			e.count(t, `
				SELECT COUNT(*) FROM users
				WHERE id=20 AND is_admin=$1 AND is_active=$2
			`, 1, tt.afterAdmin, tt.afterActive)

			e.count(t, `
				SELECT COUNT(*) FROM sessions
				WHERE user_id=20
				  AND (revoked_at IS NOT NULL)=$1
				  AND (last_rotated_at='epoch'::timestamptz)=$2
			`, 1, tt.revoked, tt.rotated)

			e.count(t, `
				SELECT COUNT(*) FROM sessions
				WHERE user_id=30
				  AND revoked_at IS NULL
				  AND last_rotated_at=created_at
			`, 1)
		})
	}
}

type failingUserSessions struct {
	delegate SessionManager
}

func (s *failingUserSessions) RevokeUserSessions(
	ctx context.Context,
	userID int64,
) error {
	if err := s.delegate.RevokeUserSessions(ctx, userID); err != nil {
		return err
	}
	return errors.New("forced failure after revocation")
}

func (s *failingUserSessions) RotateUserSessions(
	ctx context.Context,
	userID int64,
) error {
	if err := s.delegate.RotateUserSessions(ctx, userID); err != nil {
		return err
	}
	return errors.New("forced failure after rotation")
}

func TestUserSessionFailureRollsBackIntegration(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		delete bool
	}{
		{"promotion", `{"username":"changed","isAdmin":true}`, false},
		{"deactivation", `{"isActive":false}`, false},
		{"password", `{"password":"replacement123"}`, false},
		{"delete", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAdminIntegrationEnv(t)
			e.seed(t)
			e.session(t, 20, "rollback-user-session")

			var originalHash string
			e.scan(t, "SELECT password_hash FROM users WHERE id=20",
				nil, &originalHash)

			e.users = e.users.WithSessionManagerFactory(
				func(tx pgx.Tx) SessionManager {
					return &failingUserSessions{
						delegate: &transactionTestSessions{tx: tx},
					}
				},
			)

			if tt.delete {
				e.call(t, e.users.DeleteUser, "DELETE", "20", "", 500)
			} else {
				e.call(t, e.users.UpdateUser, "PATCH", "20", tt.body, 500)
			}

			e.count(t, `
				SELECT COUNT(*) FROM users
				WHERE id=20 AND username='member'
				  AND is_admin=FALSE AND is_active=TRUE
				  AND password_hash=$1
			`, 1, originalHash)

			e.count(t, `
				SELECT COUNT(*) FROM sessions
				WHERE user_id=20
				  AND revoked_at IS NULL
				  AND last_rotated_at=created_at
			`, 1)
		})
	}
}

func TestUserChangesRequireSessionManagerIntegration(t *testing.T) {
	e := newAdminIntegrationEnv(t)
	e.seed(t)
	e.session(t, 20, "missing-user-manager-session")
	e.users = e.users.WithSessionManagerFactory(nil)

	e.call(t, e.users.UpdateUser, "PATCH", "20",
		`{"isAdmin":true}`, 503)
	e.call(t, e.users.UpdateUser, "PATCH", "20",
		`{"isActive":false}`, 503)
	e.call(t, e.users.DeleteUser, "DELETE", "20", "", 503)

	e.count(t, `
		SELECT COUNT(*) FROM users
		WHERE id=20 AND is_admin=FALSE AND is_active=TRUE
	`, 1)
	e.count(t, `
		SELECT COUNT(*) FROM sessions
		WHERE user_id=20
		  AND revoked_at IS NULL
		  AND last_rotated_at=created_at
	`, 1)

	e.call(t, e.users.UpdateUser, "PATCH", "20",
		`{"username":"renamed-member","isAdmin":false,"isActive":true}`, 200)
}
