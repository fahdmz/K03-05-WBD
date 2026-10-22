package authz

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresAuthorizerIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL belum diatur")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cleanupCancel()
		_ = conn.Close(cleanupCtx)
	}()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cleanupCancel()
		_ = tx.Rollback(cleanupCtx)
	}()

	execSQL := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatalf("execute test SQL: %v", err)
		}
	}

	execSQL("CREATE SCHEMA authz_integration_test")
	execSQL("SET LOCAL search_path TO authz_integration_test")

	schema, err := os.ReadFile("../../migrations/001_init.sql")
	if err != nil {
		t.Fatalf("read database schema: %v", err)
	}
	execSQL(string(schema))

	execSQL(`
		INSERT INTO users (id, username, password_hash)
		VALUES (1, 'test-user', 'unused-test-hash');

		INSERT INTO groups (id, name)
		VALUES (1, 'viewers'), (2, 'editors');

		INSERT INTO db_connections (
			id, name, host, dbname, username, password_encrypted
		)
		VALUES
			(1, 'database-one', 'localhost', 'one', 'test', '\x00'),
			(2, 'database-two', 'localhost', 'two', 'test', '\x00');

		INSERT INTO group_db_access (
			group_id, db_connection_id, level
		)
		VALUES
			(1, 1, 'view'),
			(2, 1, 'edit');
	`)

	a := NewPostgresAuthorizer(tx)

	checkLevel := func(t *testing.T, userID, dbID int64, want Level) {
		t.Helper()

		got, err := a.EffectiveLevel(ctx, userID, dbID)
		if err != nil {
			t.Fatalf("EffectiveLevel: %v", err)
		}
		if got != want {
			t.Errorf("level = %v, want %v", got, want)
		}
	}

	t.Run("user without groups has no access", func(t *testing.T) {
		checkLevel(t, 1, 1, None)
	})

	execSQL(`
		INSERT INTO group_members (group_id, user_id)
		VALUES (1, 1)
	`)

	t.Run("viewer group gives view access", func(t *testing.T) {
		checkLevel(t, 1, 1, View)

		err := a.Require(ctx, 1, 1, Edit)
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("Require Edit = %v, want ErrForbidden", err)
		}
	})

	execSQL(`
		INSERT INTO group_members (group_id, user_id)
		VALUES (2, 1)
	`)

	t.Run("highest group access wins", func(t *testing.T) {
		checkLevel(t, 1, 1, Edit)

		if err := a.Require(ctx, 1, 1, Edit); err != nil {
			t.Fatalf("Require Edit: %v", err)
		}
	})

	t.Run("access does not carry to another database", func(t *testing.T) {
		checkLevel(t, 1, 2, None)
	})

	t.Run("unknown user has no access", func(t *testing.T) {
		checkLevel(t, 999, 1, None)
	})

	execSQL("UPDATE users SET is_active = FALSE WHERE id = 1")

	t.Run("inactive user has no access", func(t *testing.T) {
		checkLevel(t, 1, 1, None)
	})

	execSQL("UPDATE users SET is_active = TRUE WHERE id = 1")
	execSQL(`
		DELETE FROM group_db_access
		WHERE group_id = 2 AND db_connection_id = 1
	`)

	t.Run("removing edit access leaves view access", func(t *testing.T) {
		checkLevel(t, 1, 1, View)
	})

	execSQL(`
		DELETE FROM group_db_access
		WHERE group_id = 1 AND db_connection_id = 1
	`)

	t.Run("revoking all access denies requests", func(t *testing.T) {
		checkLevel(t, 1, 1, None)

		err := a.Require(ctx, 1, 1, View)
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("Require View = %v, want ErrForbidden", err)
		}
	})
}