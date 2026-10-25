package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

type deletionTestSessions struct {
	SessionManager
	calls *[]int64
	fail  bool
}

func (s *deletionTestSessions) RevokeUserSessions(ctx context.Context, id int64) error {
	*s.calls = append(*s.calls, id)
	if err := s.SessionManager.RevokeUserSessions(ctx, id); err != nil {
		return err
	}
	if s.fail {
		return errors.New("forced failure after revocation")
	}
	return nil
}

func TestResourceDeletionSessionsIntegration(t *testing.T) {
	for _, resource := range []string{"group", "database"} {
		for _, mode := range []string{"success", "failure", "missing_factory", "empty"} {
			t.Run(resource+"/"+mode, func(t *testing.T) {
				e := newAdminIntegrationEnv(t)
				e.seed(t)
				if mode != "empty" {
					e.exec(t, "INSERT INTO group_members VALUES (10,20)")
					e.exec(t, "INSERT INTO group_db_access VALUES (10,10,'view')")
					if resource == "database" {
						e.exec(t, "INSERT INTO groups(id,name,description) VALUES (11,'second','')")
						e.exec(t, "INSERT INTO group_members VALUES (11,20)")
						e.exec(t, "INSERT INTO group_db_access VALUES (11,10,'edit')")
					}
				}
				e.session(t, 20, "affected-delete-session")
				e.session(t, 30, "unaffected-delete-session")
				var calls []int64
				e.res = e.res.WithSessionManagerFactory(func(tx pgx.Tx) SessionManager {
					return &deletionTestSessions{
						SessionManager: &transactionTestSessions{tx: tx},
						calls:          &calls, fail: mode == "failure",
					}
				})
				if mode == "missing_factory" || mode == "empty" {
					e.res = e.res.WithSessionManagerFactory(nil)
				}
				status := 204
				if mode == "failure" {
					status = 500
				}
				if mode == "missing_factory" {
					status = 503
				}
				handler := e.res.DeleteGroup
				table := "groups"
				if resource == "database" {
					handler = e.res.DeleteDatabase
					table = "db_connections"
				}
				e.call(t, handler, "DELETE", "10", "", status)
				remaining := 1
				if status == 204 {
					remaining = 0
				}
				e.count(t, "SELECT COUNT(*) FROM "+table+" WHERE id=10", remaining)
				active := 1
				if mode == "success" {
					active = 0
				}
				e.count(t, "SELECT COUNT(*) FROM sessions WHERE user_id=20 AND revoked_at IS NULL", active)
				e.count(t, "SELECT COUNT(*) FROM sessions WHERE user_id=30 AND revoked_at IS NULL AND last_rotated_at=created_at", 1)
				if mode == "success" || mode == "failure" {
					if len(calls) != 1 || calls[0] != 20 {
						t.Fatalf("revoke calls = %v, want [20]", calls)
					}
				}
				if status != 204 {
					e.count(t, "SELECT COUNT(*) FROM group_members WHERE group_id=10 AND user_id=20", 1)
					e.count(t, "SELECT COUNT(*) FROM group_db_access WHERE group_id=10 AND db_connection_id=10", 1)
				}
				wantInvalidations := 0
				if resource == "database" && status == 204 {
					wantInvalidations = 1
				}
				if len(e.manager.invalidated) != wantInvalidations {
					t.Fatalf("invalidations = %v, want count %d", e.manager.invalidated, wantInvalidations)
				}
				if wantInvalidations == 1 && e.manager.invalidated[0] != 10 {
					t.Fatalf("invalidated wrong database: %v", e.manager.invalidated)
				}
			})
		}
	}
}
