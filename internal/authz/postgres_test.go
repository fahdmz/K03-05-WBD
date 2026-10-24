package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

type fakeRow struct {
	level int
	err   error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}

	*(dest[0].(*int)) = r.level
	return nil
}

type fakeDB struct {
	level int
	err   error
	args  []any
	calls int
}

func (db *fakeDB) QueryRow(
	ctx context.Context,
	sql string,
	args ...any,
) pgx.Row {
	db.calls++
	db.args = args

	return fakeRow{
		level: db.level,
		err:   db.err,
	}
}

func TestEffectiveLevel(t *testing.T) {
	queryErr := errors.New("database unavailable")

	tests := []struct {
		name    string
		dbLevel int
		dbErr   error
		want    Level
	}{
		{name: "no access", dbLevel: 0, want: None},
		{name: "view access", dbLevel: 1, want: View},
		{name: "edit access", dbLevel: 2, want: Edit},
		{name: "query failure", dbErr: queryErr, want: None},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakeDB{
				level: tt.dbLevel,
				err:   tt.dbErr,
			}
			a := NewPostgresAuthorizer(db)

			got, err := a.EffectiveLevel(
				context.Background(), 7, 3,
			)

			if !errors.Is(err, tt.dbErr) {
				t.Fatalf("error = %v, want %v", err, tt.dbErr)
			}

			if got != tt.want {
				t.Errorf("level = %v, want %v", got, tt.want)
			}

			if len(db.args) != 2 {
				t.Fatalf("query args = %v, want userID and dbID", db.args)
			}

			if db.args[0] != int64(7) || db.args[1] != int64(3) {
				t.Errorf("query args = %v, want [7 3]", db.args)
			}
		})
	}
}

func TestRequire(t *testing.T) {
	queryErr := errors.New("database unavailable")

	tests := []struct {
		name    string
		level   Level
		min     Level
		dbErr   error
		wantErr error
	}{
		{
			name:    "none cannot view",
			level:   None,
			min:     View,
			wantErr: ErrForbidden,
		},
		{
			name:    "none cannot edit",
			level:   None,
			min:     Edit,
			wantErr: ErrForbidden,
		},
		{
			name:  "view can view",
			level: View,
			min:   View,
		},
		{
			name:    "view cannot edit",
			level:   View,
			min:     Edit,
			wantErr: ErrForbidden,
		},
		{
			name:  "edit can view",
			level: Edit,
			min:   View,
		},
		{
			name:  "edit can edit",
			level: Edit,
			min:   Edit,
		},
		{
			name:    "query failure denies operation",
			min:     View,
			dbErr:   queryErr,
			wantErr: queryErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakeDB{
				level: int(tt.level),
				err:   tt.dbErr,
			}
			a := NewPostgresAuthorizer(db)

			err := a.Require(
				context.Background(), 7, 3, tt.min,
			)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestRequireRejectsInvalidMinimum(t *testing.T) {
	for _, min := range []Level{None, Level(-1), Level(3)} {
		db := &fakeDB{level: int(Edit)}
		a := NewPostgresAuthorizer(db)

		err := a.Require(context.Background(), 7, 3, min)

		if err == nil {
			t.Errorf("minimum %d: expected an error", min)
		}

		if db.calls != 0 {
			t.Errorf("minimum %d: should not query database", min)
		}
	}
}
