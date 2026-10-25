package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/jackc/pgx/v5"
)

type fakeRows struct {
	pgx.Rows
	users   []userResponse
	index   int
	scanErr error
	rowsErr error
	closed  bool
}

func (r *fakeRows) Next() bool {
	if r.index >= len(r.users) {
		return false
	}
	r.index++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}

	u := r.users[r.index-1]
	*(dest[0].(*int64)) = u.ID
	*(dest[1].(*string)) = u.Username
	*(dest[2].(*bool)) = u.IsAdmin
	*(dest[3].(*bool)) = u.IsActive
	return nil
}

func (r *fakeRows) Err() error {
	return r.rowsErr
}

func (r *fakeRows) Close() {
	r.closed = true
}

type fakeQuerier struct {
	rows  *fakeRows
	err   error
	calls int
}

func (db *fakeQuerier) Query(
	ctx context.Context,
	sql string,
	args ...any,
) (pgx.Rows, error) {
	db.calls++
	if db.err != nil {
		return nil, db.err
	}
	return db.rows, nil
}

func TestListUsers(t *testing.T) {
	dbErr := errors.New("private database error")

	sampleUsers := []userResponse{
		{ID: 1, Username: "admin", IsAdmin: true, IsActive: true},
		{ID: 2, Username: "member", IsAdmin: false, IsActive: false},
	}

	tests := []struct {
		name      string
		user      *auth.User
		rows      *fakeRows
		queryErr  error
		wantCode  int
		wantCalls int
		wantUsers []userResponse
	}{
		{
			name:     "not logged in",
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "non admin denied",
			user:     &auth.User{ID: 2, IsAdmin: false},
			wantCode: http.StatusForbidden,
		},
		{
			name:      "admin gets users",
			user:      &auth.User{ID: 1, IsAdmin: true},
			rows:      &fakeRows{users: sampleUsers},
			wantCode:  http.StatusOK,
			wantCalls: 1,
			wantUsers: sampleUsers,
		},
		{
			name:      "empty list is an array",
			user:      &auth.User{ID: 1, IsAdmin: true},
			rows:      &fakeRows{},
			wantCode:  http.StatusOK,
			wantCalls: 1,
			wantUsers: []userResponse{},
		},
		{
			name:      "query failure",
			user:      &auth.User{ID: 1, IsAdmin: true},
			queryErr:  dbErr,
			wantCode:  http.StatusInternalServerError,
			wantCalls: 1,
		},
		{
			name:      "scan failure",
			user:      &auth.User{ID: 1, IsAdmin: true},
			rows:      &fakeRows{users: sampleUsers, scanErr: dbErr},
			wantCode:  http.StatusInternalServerError,
			wantCalls: 1,
		},
		{
			name:      "iteration failure after reading data",
			user:      &auth.User{ID: 1, IsAdmin: true},
			rows:      &fakeRows{users: sampleUsers, rowsErr: dbErr},
			wantCode:  http.StatusInternalServerError,
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakeQuerier{
				rows: tt.rows,
				err:  tt.queryErr,
			}
			handler := NewHandler(db)

			req := httptest.NewRequest(
				http.MethodGet, "/api/admin/users", nil,
			)
			if tt.user != nil {
				ctx := auth.WithUser(req.Context(), tt.user)
				req = req.WithContext(ctx)
			}

			rec := httptest.NewRecorder()
			handler.ListUsers(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf(
					"status = %d, want %d; body = %s",
					rec.Code, tt.wantCode, rec.Body.String(),
				)
			}

			if db.calls != tt.wantCalls {
				t.Errorf(
					"query calls = %d, want %d",
					db.calls, tt.wantCalls,
				)
			}

			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}

			if tt.rows != nil && !tt.rows.closed {
				t.Error("query rows were not closed")
			}

			if tt.wantCode != http.StatusOK {
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode error response: %v", err)
				}

				message, ok := body["error"].(string)
				if !ok || message == "" {
					t.Errorf("expected nonempty error message, got %v", body)
				}

				if strings.Contains(rec.Body.String(), dbErr.Error()) {
					t.Error("response exposes internal database error")
				}
				return
			}

			var got []userResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode users: %v", err)
			}

			if !reflect.DeepEqual(got, tt.wantUsers) {
				t.Errorf("users = %#v, want %#v", got, tt.wantUsers)
			}

			var rawUsers []map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &rawUsers); err != nil {
				t.Fatalf("decode response fields: %v", err)
			}

			allowedFields := map[string]bool{
				"id":       true,
				"username": true,
				"isAdmin":  true,
				"isActive": true,
			}

			for _, user := range rawUsers {
				for field := range user {
					if !allowedFields[field] {
						t.Errorf("unexpected response field: %s", field)
					}
				}
			}
		})
	}
}
