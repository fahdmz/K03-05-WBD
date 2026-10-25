package admin

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

func TestAdminEndpointsRejectUnauthorized(t *testing.T) {
	users := NewHandler(nil)
	res := &ResourceHandler{}

	endpoints := []struct {
		name, method string
		handler      http.HandlerFunc
	}{
		{"list users", "GET", users.ListUsers},
		{"create user", "POST", users.CreateUser},
		{"update user", "PATCH", users.UpdateUser},
		{"delete user", "DELETE", users.DeleteUser},
		{"list groups", "GET", res.ListGroups},
		{"create group", "POST", res.CreateGroup},
		{"update group", "PATCH", res.UpdateGroup},
		{"delete group", "DELETE", res.DeleteGroup},
		{"group members", "PUT", res.SetGroupMembers},
		{"group access", "PUT", res.SetGroupAccess},
		{"list databases", "GET", res.ListDatabases},
		{"create database", "POST", res.CreateDatabase},
		{"update database", "PATCH", res.UpdateDatabase},
		{"delete database", "DELETE", res.DeleteDatabase},
		{"test database", "POST", res.TestDatabase},
	}

	actors := []struct {
		name string
		user *auth.User
		want int
	}{
		{"anonymous", nil, 401},
		{"non admin", &auth.User{ID: 20}, 403},
	}

	for _, endpoint := range endpoints {
		for _, actor := range actors {
			t.Run(endpoint.name+"/"+actor.name, func(t *testing.T) {
				rec := adminTestRequest(
					endpoint.handler, endpoint.method, "20", `{}`, actor.user,
				)
				adminTestStatus(t, rec, actor.want)
			})
		}
	}
}

func TestAdminValidationBeforeDatabase(t *testing.T) {
	users := NewHandler(nil)
	res := &ResourceHandler{}
	admin := &auth.User{ID: 10, IsAdmin: true}

	tests := []struct {
		name, method, id, body string
		handler                http.HandlerFunc
		want                   int
	}{
		{"broken JSON", "POST", "", `{`, users.CreateUser, 400},
		{"unknown field", "POST", "",
			`{"username":"alice","password":"password123","extra":1}`,
			users.CreateUser, 400},
		{"two JSON objects", "POST", "",
			`{"username":"alice","password":"password123"} {}`,
			users.CreateUser, 400},
		{"empty username", "POST", "",
			`{"username":" ","password":"password123"}`,
			users.CreateUser, 400},
		{"short password", "POST", "",
			`{"username":"alice","password":"short"}`,
			users.CreateUser, 400},
		{"long password", "POST", "",
			`{"username":"alice","password":"` + strings.Repeat("a", 73) + `"}`,
			users.CreateUser, 400},
		{"empty user patch", "PATCH", "20", `{}`, users.UpdateUser, 400},
		{"invalid user ID", "PATCH", "abc", `{}`, users.UpdateUser, 400},
		{"self disable", "PATCH", "10",
			`{"isActive":false}`, users.UpdateUser, 409},
		{"self demote", "PATCH", "10",
			`{"isAdmin":false}`, users.UpdateUser, 409},
		{"self delete", "DELETE", "10", "", users.DeleteUser, 409},
		{"invalid delete ID", "DELETE", "-1", "", users.DeleteUser, 400},
		{"empty group name", "POST", "", `{"name":" "}`, res.CreateGroup, 400},
		{"empty group patch", "PATCH", "10", `{}`, res.UpdateGroup, 400},
		{"members missing", "PUT", "10", `{}`, res.SetGroupMembers, 400},
		{"duplicate members", "PUT", "10",
			`{"userIds":[20,20]}`, res.SetGroupMembers, 400},
		{"negative member", "PUT", "10",
			`{"userIds":[-1]}`, res.SetGroupMembers, 400},
		{"access missing", "PUT", "10", `{}`, res.SetGroupAccess, 400},
		{"invalid level", "PUT", "10",
			`{"access":[{"databaseId":10,"level":"owner"}]}`,
			res.SetGroupAccess, 400},
		{"duplicate access", "PUT", "10",
			`{"access":[{"databaseId":10,"level":"view"},{"databaseId":10,"level":"edit"}]}`,
			res.SetGroupAccess, 400},
		{"invalid database create", "POST", "", `{}`, res.CreateDatabase, 400},
		{"empty database patch", "PATCH", "10", `{}`, res.UpdateDatabase, 400},
		{"invalid connection test", "POST", "",
			`{"host":"localhost","port":70000,"dbname":"shop","username":"test"}`,
			res.TestDatabase, 400},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := adminTestRequest(tt.handler, tt.method, tt.id, tt.body, admin)
			adminTestStatus(t, rec, tt.want)
		})
	}
}

type mutationTestDB struct {
	rows  *fakeRows
	err   error
	args  []any
	calls int
}

func (db *mutationTestDB) Query(
	ctx context.Context, sql string, args ...any,
) (pgx.Rows, error) {
	db.calls++
	db.args = args
	if db.err != nil {
		return nil, db.err
	}
	return db.rows, nil
}

func TestUserMutationsUnit(t *testing.T) {
	admin := &auth.User{ID: 10, IsAdmin: true}

	operations := []struct {
		name, method, body string
		success            int
		handler            func(*Handler) http.HandlerFunc
	}{
		{"create", "POST",
			`{"username":" alice ","password":"password123","isAdmin":false}`,
			201, func(h *Handler) http.HandlerFunc { return h.CreateUser }},
	}

	failures := []struct {
		name string
		err  error
		code int
	}{
		{"success", nil, 0},
		{"database error", errors.New("private-db-detail"), 500},
		{"duplicate", &pgconn.PgError{
			Code: "23505", ConstraintName: "users_username_key",
		}, 409},
		{"foreign key", &pgconn.PgError{Code: "23503"}, 409},
		{"missing", pgx.ErrNoRows, 404},
	}

	for _, op := range operations {
		for _, failure := range failures {
			t.Run(op.name+"/"+failure.name, func(t *testing.T) {
				db := &mutationTestDB{
					err: failure.err,
					rows: &fakeRows{users: []userResponse{
						{ID: 20, Username: "alice", IsActive: true},
					}},
				}

				rec := adminTestRequest(
					op.handler(NewHandler(db)), op.method, "20", op.body, admin,
				)
				want := failure.code
				if failure.err == nil {
					want = op.success
				}
				adminTestStatus(t, rec, want)

				if db.calls != 1 {
					t.Fatalf("query calls = %d, want 1", db.calls)
				}
				if strings.Contains(rec.Body.String(), "private-db-detail") {
					t.Fatal("response exposes database details")
				}

				if failure.err == nil {
					if !db.rows.closed {
						t.Error("rows were not closed")
					}
					if op.name != "delete" {
						adminTestNoSecrets(t, rec)
						user := adminTestDecode[userResponse](t, rec)
						if user.ID != 20 || user.Username != "alice" {
							t.Fatalf("unexpected response: %+v", user)
						}
					}

					if op.name == "create" {
						if db.args[0] != "alice" {
							t.Error("username was not trimmed")
						}
						hash, ok := db.args[1].(string)
						if !ok {
							t.Fatal("password hash argument is not a string")
						}
						if err := bcrypt.CompareHashAndPassword(
							[]byte(hash), []byte("password123"),
						); err != nil {
							t.Fatalf("invalid password hash: %v", err)
						}
					}
				}
			})
		}
	}
}

func TestPasswordEncryptionUnit(t *testing.T) {
	h := &ResourceHandler{aead: adminTestAEAD(t)}
	first, err := h.encryptPassword("secret-password")
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.encryptPassword("secret-password")
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(first, second) {
		t.Fatal("repeated encryption reused the same ciphertext")
	}

	n := h.aead.NonceSize()
	plain, err := h.aead.Open(nil, first[:n], first[n:], nil)
	if err != nil || string(plain) != "secret-password" {
		t.Fatalf("decrypt failed: %v", err)
	}

	first[len(first)-1] ^= 1
	if _, err := h.aead.Open(nil, first[:n], first[n:], nil); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}

func TestDatabaseConnectionHandlerUnit(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(strconv.FormatBool(fails), func(t *testing.T) {
			manager := &adminTestManager{}
			if fails {
				manager.testErr = errors.New("private-password-in-driver-error")
			}
			h := &ResourceHandler{manager: manager}
			rec := adminTestRequest(
				h.TestDatabase, "POST", "",
				`{"host":"localhost","dbname":"shop","username":"target","password":"secret"}`,
				&auth.User{ID: 10, IsAdmin: true},
			)

			want := 200
			if fails {
				want = 502
			}
			adminTestStatus(t, rec, want)

			if manager.testCalls != 1 ||
				manager.lastConfig.Host != "localhost" ||
				manager.lastConfig.Port != 5432 ||
				manager.lastConfig.Password != "secret" ||
				manager.lastConfig.SSLMode != "disable" {
				t.Fatalf("unexpected manager call: %+v", manager)
			}

			if strings.Contains(rec.Body.String(), "private-password") ||
				strings.Contains(rec.Body.String(), "secret") {
				t.Fatal("response exposes connection secrets")
			}

			if !fails {
				body := adminTestDecode[map[string]bool](t, rec)
				if !body["ok"] {
					t.Fatal("expected ok=true")
				}
			}
		})
	}
}
