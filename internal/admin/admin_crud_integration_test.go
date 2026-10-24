package admin

import (
	"bytes"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestUserCRUDIntegration(t *testing.T) {
	e := newAdminIntegrationEnv(t)
	e.seed(t)

	rec := e.call(t, e.users.CreateUser, "POST", "",
		`{"username":"alice","password":"password123","isAdmin":false}`, 201)
	user := adminTestDecode[userResponse](t, rec)
	adminTestNoSecrets(t, rec)

	if user.Username != "alice" || user.IsAdmin || !user.IsActive {
		t.Fatalf("unexpected created user: %+v", user)
	}
	id := strconv.FormatInt(user.ID, 10)

	var hash string
	e.scan(t, "SELECT password_hash FROM users WHERE id=$1",
		[]any{user.ID}, &hash)
	if err := bcrypt.CompareHashAndPassword(
		[]byte(hash), []byte("password123"),
	); err != nil {
		t.Fatal(err)
	}

	e.call(t, e.users.CreateUser, "POST", "",
		`{"username":"alice","password":"password123"}`, 409)

	e.session(t, user.ID, "alice-session")
	rec = e.call(t, e.users.UpdateUser, "PATCH", id,
		`{"username":"alice-renamed"}`, 200)
	updated := adminTestDecode[userResponse](t, rec)
	if updated.Username != "alice-renamed" || !updated.IsActive {
		t.Fatalf("unexpected updated user: %+v", updated)
	}
	e.count(t, `SELECT COUNT(*) FROM sessions
		WHERE user_id=$1 AND revoked_at IS NULL`, 1, user.ID)

	rec = e.call(t, e.users.UpdateUser, "PATCH", id,
		`{"password":"new-password123","isActive":false}`, 200)
	updated = adminTestDecode[userResponse](t, rec)
	if updated.IsActive || updated.Username != "alice-renamed" {
		t.Fatalf("partial update did not preserve fields: %+v", updated)
	}
	e.scan(t, "SELECT password_hash FROM users WHERE id=$1",
		[]any{user.ID}, &hash)
	if err := bcrypt.CompareHashAndPassword(
		[]byte(hash), []byte("new-password123"),
	); err != nil {
		t.Fatal(err)
	}
	e.count(t, `SELECT COUNT(*) FROM sessions
		WHERE user_id=$1 AND revoked_at IS NULL`, 0, user.ID)

	e.call(t, e.users.UpdateUser, "PATCH", id,
		`{"username":"member"}`, 409)
	e.count(t, "SELECT COUNT(*) FROM users WHERE username='alice-renamed'", 1)

	e.call(t, e.users.UpdateUser, "PATCH", "99999",
		`{"username":"missing"}`, 404)
	e.call(t, e.users.DeleteUser, "DELETE", "99999", "", 404)

	e.exec(t, "INSERT INTO group_members(group_id,user_id) VALUES(10,$1)", user.ID)
	e.call(t, e.users.DeleteUser, "DELETE", id, "", 204)
	e.count(t, "SELECT COUNT(*) FROM users WHERE id=$1", 0, user.ID)
	e.count(t, "SELECT COUNT(*) FROM sessions WHERE user_id=$1", 0, user.ID)
	e.count(t, "SELECT COUNT(*) FROM group_members WHERE user_id=$1", 0, user.ID)

	e.session(t, 20, "protected-user-session")
	e.exec(t, `
		INSERT INTO migrations (
			db_connection_id, title, sql, status, created_by
		) VALUES (10, 'draft', 'SELECT 1', 'draft', 20)
	`)
	e.call(t, e.users.DeleteUser, "DELETE", "20", "", 409)
	e.count(t, "SELECT COUNT(*) FROM users WHERE id=20", 1)
	e.count(t, `SELECT COUNT(*) FROM sessions
		WHERE user_id=20 AND revoked_at IS NULL`, 1)

	e.call(t, e.users.UpdateUser, "PATCH", "20", `{"isActive":false}`, 200)
	e.count(t, "SELECT COUNT(*) FROM users WHERE id=20 AND is_active=FALSE", 1)
}

func TestGroupCRUDIntegration(t *testing.T) {
	e := newAdminIntegrationEnv(t)

	rec := e.call(t, e.res.ListGroups, "GET", "", "", 200)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("expected [], got %s", rec.Body.String())
	}

	e.seed(t)
	rec = e.call(t, e.res.CreateGroup, "POST", "",
		`{"name":" developers ","description":"initial"}`, 201)
	group := adminTestDecode[groupResponse](t, rec)
	if group.Name != "developers" || group.Description != "initial" {
		t.Fatalf("unexpected group: %+v", group)
	}
	id := strconv.FormatInt(group.ID, 10)

	e.call(t, e.res.CreateGroup, "POST", "",
		`{"name":"developers"}`, 409)

	rec = e.call(t, e.res.UpdateGroup, "PATCH", id,
		`{"description":"updated"}`, 200)
	group = adminTestDecode[groupResponse](t, rec)
	if group.Name != "developers" || group.Description != "updated" {
		t.Fatalf("unexpected patch result: %+v", group)
	}

	e.call(t, e.res.UpdateGroup, "PATCH", id, `{"name":"team"}`, 409)
	e.count(t, "SELECT COUNT(*) FROM groups WHERE name='developers'", 1)

	rec = e.call(t, e.res.ListGroups, "GET", "", "", 200)
	groups := adminTestDecode[[]groupResponse](t, rec)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}

	e.exec(t, "INSERT INTO group_members VALUES($1,20)", group.ID)
	e.exec(t, "INSERT INTO group_db_access VALUES($1,10,'view')", group.ID)
	e.session(t, 20, "delete-group-session")

	e.call(t, e.res.DeleteGroup, "DELETE", id, "", 204)
	e.count(t, "SELECT COUNT(*) FROM groups WHERE id=$1", 0, group.ID)
	e.count(t, "SELECT COUNT(*) FROM group_members WHERE group_id=$1", 0, group.ID)
	e.count(t, "SELECT COUNT(*) FROM group_db_access WHERE group_id=$1", 0, group.ID)
	e.count(t, `SELECT COUNT(*) FROM sessions
		WHERE user_id=20 AND revoked_at IS NULL`, 0)

	e.call(t, e.res.UpdateGroup, "PATCH", "99999", `{"name":"missing"}`, 404)
	e.call(t, e.res.DeleteGroup, "DELETE", "99999", "", 404)
}

func TestGroupMembersIntegration(t *testing.T) {
	e := newAdminIntegrationEnv(t)
	e.seed(t)
	e.exec(t, "INSERT INTO group_members VALUES(10,20)")
	e.session(t, 20, "old-member-session")
	e.session(t, 30, "new-member-session")

	e.call(t, e.res.SetGroupMembers, "PUT", "10", `{"userIds":[30]}`, 200)
	e.count(t, "SELECT COUNT(*) FROM group_members WHERE group_id=10", 1)
	e.count(t, "SELECT COUNT(*) FROM group_members WHERE group_id=10 AND user_id=30", 1)
	e.count(t, `SELECT COUNT(*) FROM sessions WHERE user_id = 20 AND revoked_at IS NOT NULL`, 1)
	e.count(t, `SELECT COUNT(*) FROM sessions WHERE user_id = 30 AND revoked_at IS NULL AND last_rotated_at = 'epoch'::timestamptz`, 1)
	e.call(t, e.res.SetGroupMembers, "PUT", "10", `{"userIds":[99999]}`, 400)
	e.count(t, "SELECT COUNT(*) FROM group_members WHERE group_id=10 AND user_id=30", 1)

	e.call(t, e.res.SetGroupMembers, "PUT", "99999", `{"userIds":[]}`, 404)
	e.call(t, e.res.SetGroupMembers, "PUT", "10", `{"userIds":[]}`, 200)
	e.count(t, "SELECT COUNT(*) FROM group_members WHERE group_id=10", 0)
}

func TestGroupMembersRollbackIntegration(t *testing.T) {
	e := newAdminIntegrationEnv(t)
	e.seed(t)
	e.exec(t, "INSERT INTO group_members VALUES(10,20)")
	e.session(t, 20, "rollback-member-session")

	// Inject a failure after the handler has revoked sessions and deleted members.
	e.exec(t, `
		CREATE FUNCTION reject_test_member() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.user_id = 30 THEN
				RAISE EXCEPTION 'forced test failure';
			END IF;
			RETURN NEW;
		END;
		$$;

		CREATE TRIGGER reject_test_member
		BEFORE INSERT ON group_members
		FOR EACH ROW EXECUTE FUNCTION reject_test_member();
	`)

	e.call(t, e.res.SetGroupMembers, "PUT", "10", `{"userIds":[30]}`, 500)

	e.count(t, "SELECT COUNT(*) FROM group_members WHERE group_id=10 AND user_id=20", 1)
	e.count(t, "SELECT COUNT(*) FROM group_members WHERE group_id=10 AND user_id=30", 0)
	e.count(t, `SELECT COUNT(*) FROM sessions
		WHERE user_id=20 AND revoked_at IS NULL`, 1)
}

func TestGroupAccessIntegration(t *testing.T) {
	e := newAdminIntegrationEnv(t)
	e.seed(t)
	e.exec(t, "INSERT INTO group_members VALUES(10,20)")
	e.exec(t, "INSERT INTO group_db_access VALUES(10,10,'edit')")
	e.session(t, 20, "access-session")

	e.call(t, e.res.SetGroupAccess, "PUT", "10",
		`{"access":[{"databaseId":10,"level":"view"}]}`, 200)

	var level string
	e.scan(t, "SELECT level FROM group_db_access WHERE group_id=10 AND db_connection_id=10",
		nil, &level)
	if level != "view" {
		t.Fatalf("level = %s, want view", level)
	}
	e.count(t, `SELECT COUNT(*) FROM sessions
		WHERE user_id=20 AND revoked_at IS NULL`, 0)

	e.call(t, e.res.SetGroupAccess, "PUT", "10",
		`{"access":[{"databaseId":99999,"level":"view"}]}`, 400)
	e.count(t, "SELECT COUNT(*) FROM group_db_access WHERE group_id=10", 1)

	e.session(t, 20, "access-rollback-session")
	e.exec(t, `
		CREATE FUNCTION reject_test_access() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.level = 'edit' THEN
				RAISE EXCEPTION 'forced access failure';
			END IF;
			RETURN NEW;
		END;
		$$;

		CREATE TRIGGER reject_test_access
		BEFORE INSERT ON group_db_access
		FOR EACH ROW EXECUTE FUNCTION reject_test_access();
	`)

	e.call(t, e.res.SetGroupAccess, "PUT", "10",
		`{"access":[{"databaseId":10,"level":"edit"}]}`, 500)
	e.count(t, `SELECT COUNT(*) FROM group_db_access
		WHERE group_id=10 AND level='view'`, 1)
	e.count(t, `SELECT COUNT(*) FROM sessions
		WHERE current_token_hash='access-rollback-session' AND revoked_at IS NULL`, 1)

	e.call(t, e.res.SetGroupAccess, "PUT", "99999", `{"access":[]}`, 404)
	e.call(t, e.res.SetGroupAccess, "PUT", "10", `{"access":[]}`, 200)
	e.count(t, "SELECT COUNT(*) FROM group_db_access WHERE group_id=10", 0)
	e.count(t, `SELECT COUNT(*) FROM sessions
		WHERE user_id=20 AND revoked_at IS NULL`, 0)
}

func TestDatabaseCRUDIntegration(t *testing.T) {
	e := newAdminIntegrationEnv(t)

	rec := e.call(t, e.res.ListDatabases, "GET", "", "", 200)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("expected [], got %s", rec.Body.String())
	}
	e.seed(t)

	body := `{
		"name":"new-db","host":"localhost","port":5433,
		"dbname":"shop","username":"target","password":"db-secret",
		"sslmode":"disable"
	}`
	rec = e.call(t, e.res.CreateDatabase, "POST", "", body, 201)
	item := adminTestDecode[databaseResponse](t, rec)
	adminTestNoSecrets(t, rec)
	if item.Name != "new-db" || item.Port != 5433 {
		t.Fatalf("unexpected connection: %+v", item)
	}
	id := strconv.FormatInt(item.ID, 10)

	var encrypted []byte
	e.scan(t, "SELECT password_encrypted FROM db_connections WHERE id=$1",
		[]any{item.ID}, &encrypted)

	n := e.res.aead.NonceSize()
	if len(encrypted) < n+e.res.aead.Overhead() {
		t.Fatal("stored password is not valid ciphertext")
	}
	plain, err := e.res.aead.Open(nil, encrypted[:n], encrypted[n:], nil)
	if err != nil || string(plain) != "db-secret" {
		t.Fatalf("stored password failed decryption: %v", err)
	}

	e.call(t, e.res.CreateDatabase, "POST", "", body, 409)

	rec = e.call(t, e.res.UpdateDatabase, "PATCH", id, `{"name":"renamed-db"}`, 200)
	adminTestNoSecrets(t, rec)
	updated := adminTestDecode[databaseResponse](t, rec)
	if updated.Name != "renamed-db" || updated.Host != "localhost" || updated.Port != 5433 {
		t.Fatalf("unexpected partial update: %+v", updated)
	}

	var unchanged []byte
	e.scan(t, "SELECT password_encrypted FROM db_connections WHERE id=$1",
		[]any{item.ID}, &unchanged)
	if !bytes.Equal(encrypted, unchanged) {
		t.Fatal("omitting password changed stored ciphertext")
	}

	e.call(t, e.res.UpdateDatabase, "PATCH", id, `{"password":""}`, 200)
	e.scan(t, "SELECT password_encrypted FROM db_connections WHERE id=$1",
		[]any{item.ID}, &encrypted)
	plain, err = e.res.aead.Open(nil, encrypted[:n], encrypted[n:], nil)
	if err != nil || len(plain) != 0 {
		t.Fatalf("empty replacement password was not preserved: %v", err)
	}

	beforeInvalidations := len(e.manager.invalidated)
	e.call(t, e.res.UpdateDatabase, "PATCH", id, `{"name":"fixture-db"}`, 409)
	e.call(t, e.res.UpdateDatabase, "PATCH", id, `{"port":0}`, 400)
	e.call(t, e.res.UpdateDatabase, "PATCH", "99999", `{"name":"missing"}`, 404)
	e.call(t, e.res.DeleteDatabase, "DELETE", "99999", "", 404)
	if len(e.manager.invalidated) != beforeInvalidations {
		t.Fatal("failed mutation invalidated a connection")
	}

	e.count(t, "SELECT COUNT(*) FROM db_connections WHERE id=$1 AND name='renamed-db'", 1, item.ID)

	rec = e.call(t, e.res.ListDatabases, "GET", "", "", 200)
	adminTestNoSecrets(t, rec)
	items := adminTestDecode[[]databaseResponse](t, rec)
	if len(items) != 2 {
		t.Fatalf("got %d connections, want 2", len(items))
	}

	e.exec(t, "INSERT INTO group_members VALUES(10,20)")
	e.exec(t, "INSERT INTO group_db_access VALUES(10,$1,'view')", item.ID)
	e.exec(t, `
		INSERT INTO migrations(db_connection_id,title,sql,status,created_by)
		VALUES($1,'draft','SELECT 1','draft',20)
	`, item.ID)
	e.session(t, 20, "database-delete-session")

	e.call(t, e.res.DeleteDatabase, "DELETE", id, "", 204)
	e.count(t, "SELECT COUNT(*) FROM db_connections WHERE id=$1", 0, item.ID)
	e.count(t, "SELECT COUNT(*) FROM group_db_access WHERE db_connection_id=$1", 0, item.ID)
	e.count(t, "SELECT COUNT(*) FROM migrations WHERE db_connection_id=$1", 0, item.ID)
	e.count(t, "SELECT COUNT(*) FROM sessions WHERE user_id=20 AND revoked_at IS NULL", 0)

	wantInvalidations := []int64{item.ID, item.ID, item.ID}
	if !reflect.DeepEqual(e.manager.invalidated, wantInvalidations) {
		t.Fatalf("invalidations = %v, want %v",
			e.manager.invalidated, wantInvalidations)
	}
}

func TestResourceDatabaseFailuresIntegration(t *testing.T) {
	e := newAdminIntegrationEnv(t)
	e.seed(t)

	e.exec(t, "DROP TABLE groups CASCADE")
	e.exec(t, "DROP TABLE db_connections CASCADE")

	cases := []struct {
		name, method, id, body string
		handler                http.HandlerFunc
	}{
		{"list groups", "GET", "", "", e.res.ListGroups},
		{"create group", "POST", "", `{"name":"new"}`, e.res.CreateGroup},
		{"update group", "PATCH", "10", `{"name":"new"}`, e.res.UpdateGroup},
		{"delete group", "DELETE", "10", "", e.res.DeleteGroup},
		{"members", "PUT", "10", `{"userIds":[]}`, e.res.SetGroupMembers},
		{"access", "PUT", "10", `{"access":[]}`, e.res.SetGroupAccess},
		{"list databases", "GET", "", "", e.res.ListDatabases},
		{"create database", "POST", "",
			`{"name":"new","host":"localhost","dbname":"shop","username":"test","password":"secret"}`,
			e.res.CreateDatabase},
		{"update database", "PATCH", "10", `{"name":"new"}`, e.res.UpdateDatabase},
		{"delete database", "DELETE", "10", "", e.res.DeleteDatabase},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.call(t, tc.handler, tc.method, tc.id, tc.body, 500)
			for _, detail := range []string{"SQLSTATE", "does not exist", "password"} {
				if strings.Contains(rec.Body.String(), detail) {
					t.Fatalf("database detail exposed: %s", rec.Body.String())
				}
			}
		})
	}

	if len(e.manager.invalidated) != 0 {
		t.Fatal("failed database operations invalidated pools")
	}
}

func TestResourceTransactionRollbackIntegration(t *testing.T) {
	e := newAdminIntegrationEnv(t)
	e.seed(t)
	e.exec(t, "INSERT INTO group_members VALUES(10,20)")
	e.exec(t, "INSERT INTO group_db_access VALUES(10,10,'view')")
	e.session(t, 20, "delete-rollback-session")

	for _, table := range []string{"groups", "db_connections"} {
		e.exec(t, fmt.Sprintf(`
			CREATE FUNCTION reject_delete_%[1]s() RETURNS trigger
			LANGUAGE plpgsql AS $$
			BEGIN
				RAISE EXCEPTION 'forced delete failure';
			END;
			$$;

			CREATE TRIGGER reject_delete
			BEFORE DELETE ON %[1]s
			FOR EACH ROW EXECUTE FUNCTION reject_delete_%[1]s();
		`, table))
	}

	e.call(t, e.res.DeleteGroup, "DELETE", "10", "", 500)
	e.call(t, e.res.DeleteDatabase, "DELETE", "10", "", 500)

	e.count(t, "SELECT COUNT(*) FROM groups WHERE id=10", 1)
	e.count(t, "SELECT COUNT(*) FROM db_connections WHERE id=10", 1)
	e.count(t, "SELECT COUNT(*) FROM group_db_access WHERE group_id=10", 1)
	e.count(t, "SELECT COUNT(*) FROM sessions WHERE revoked_at IS NULL", 1)

	if len(e.manager.invalidated) != 0 {
		t.Fatal("rolled-back deletion invalidated connection")
	}
}

func TestListGroupsDetailsIntegration(t *testing.T) {
	e := newAdminIntegrationEnv(t)
	e.seed(t)

	e.exec(t, `
		INSERT INTO groups (id, name, description)
		VALUES
			(11, 'empty-group', ''),
			(12, 'other-group', 'separate');

		INSERT INTO db_connections (
			id, name, host, dbname, username, password_encrypted
		)
		VALUES (
			11, 'second-db', 'localhost', 'other', 'target', '\x00'
		);

		INSERT INTO group_members (group_id, user_id)
		VALUES
			(10, 30),
			(10, 20),
			(12, 10);

		INSERT INTO group_db_access (
			group_id, db_connection_id, level
		)
		VALUES
			(10, 11, 'edit'),
			(10, 10, 'view'),
			(12, 11, 'view');
	`)

	rec := e.call(t, e.res.ListGroups, "GET", "", "", 200)

	// Explicit JSON fields also verify the public response contract.
	type listedGroup struct {
		ID          int64              `json:"id"`
		Name        string             `json:"name"`
		Description string             `json:"description"`
		MemberIDs   []int64            `json:"memberIds"`
		Access      []groupAccessEntry `json:"access"`
	}

	got := adminTestDecode[[]listedGroup](t, rec)

	want := []listedGroup{
		{
			ID:          10,
			Name:        "team",
			Description: "original",
			MemberIDs:   []int64{20, 30},
			Access: []groupAccessEntry{
				{DatabaseID: 10, Level: "view"},
				{DatabaseID: 11, Level: "edit"},
			},
		},
		{
			ID:          11,
			Name:        "empty-group",
			Description: "",
			MemberIDs:   []int64{},
			Access:      []groupAccessEntry{},
		},
		{
			ID:          12,
			Name:        "other-group",
			Description: "separate",
			MemberIDs:   []int64{10},
			Access: []groupAccessEntry{
				{DatabaseID: 11, Level: "view"},
			},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %#v, want %#v", got, want)
	}
}