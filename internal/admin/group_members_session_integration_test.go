package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

type failingMemberSessions struct {
	delegate SessionManager
}

func (s *failingMemberSessions) RevokeUserSessions(
	ctx context.Context,
	userID int64,
) error {
	return s.delegate.RevokeUserSessions(ctx, userID)
}

func (s *failingMemberSessions) RotateUserSessions(
	ctx context.Context,
	userID int64,
) error {
	return errors.New("forced session failure")
}

func TestMemberSessionFailureRollsBackIntegration(t *testing.T) {
	e := newAdminIntegrationEnv(t)
	e.seed(t)
	e.exec(t, "INSERT INTO group_members VALUES (10, 20)")
	e.session(t, 20, "existing-member-session")
	e.session(t, 30, "incoming-member-session")

	e.res = e.res.WithSessionManagerFactory(func(tx pgx.Tx) SessionManager {
		return &failingMemberSessions{
			delegate: &transactionTestSessions{tx: tx},
		}
	})

	e.call(t, e.res.SetGroupMembers, "PUT", "10",
		`{"userIds":[30]}`, 500)

	e.count(t, `
		SELECT COUNT(*) FROM group_members
		WHERE group_id = 10 AND user_id = 20
	`, 1)

	e.count(t, `
		SELECT COUNT(*) FROM group_members
		WHERE group_id = 10 AND user_id = 30
	`, 0)

	e.count(t, `
		SELECT COUNT(*) FROM sessions WHERE revoked_at IS NULL
	`, 2)
}

func TestMemberChangesRequireSessionManagerIntegration(t *testing.T) {
	e := newAdminIntegrationEnv(t)
	e.seed(t)
	e.exec(t, "INSERT INTO group_members VALUES (10, 20)")

	e.res = e.res.WithSessionManagerFactory(nil)

	e.call(t, e.res.SetGroupMembers, "PUT", "10",
		`{"userIds":[30]}`, 503)

	e.count(t, `
		SELECT COUNT(*) FROM group_members
		WHERE group_id = 10 AND user_id = 20
	`, 1)

	e.count(t, `
		SELECT COUNT(*) FROM group_members
		WHERE group_id = 10 AND user_id = 30
	`, 0)
}

func TestUnchangedMembersPreserveSessionsIntegration(t *testing.T) {
	e := newAdminIntegrationEnv(t)
	e.seed(t)
	e.exec(t, "INSERT INTO group_members VALUES (10, 20)")
	e.session(t, 20, "unchanged-member-session")

	e.call(t, e.res.SetGroupMembers, "PUT", "10",
		`{"userIds":[20]}`, 200)

	e.count(t, `
		SELECT COUNT(*) FROM sessions
		WHERE user_id = 20
		  AND revoked_at IS NULL
		  AND last_rotated_at = created_at
	`, 1)
}
