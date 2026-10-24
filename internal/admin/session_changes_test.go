package admin

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type sessionCall struct {
	action string
	userID int64
}

type sessionManagerSpy struct {
	calls []sessionCall
	err   error
}

func (s *sessionManagerSpy) RevokeUserSessions(
	ctx context.Context,
	userID int64,
) error {
	s.calls = append(s.calls, sessionCall{"revoke", userID})
	return s.err
}

func (s *sessionManagerSpy) RotateUserSessions(
	ctx context.Context,
	userID int64,
) error {
	s.calls = append(s.calls, sessionCall{"rotate", userID})
	return s.err
}

func TestMembershipSessionChanges(t *testing.T) {
	tests := []struct {
		name   string
		before []int64
		after  []int64
		want   []sessionCall
	}{
		{
			name:   "unchanged",
			before: []int64{20, 30},
			after:  []int64{30, 20},
		},
		{
			name:   "new member rotates",
			before: []int64{20},
			after:  []int64{20, 30},
			want:   []sessionCall{{"rotate", 30}},
		},
		{
			name:   "removed member revoked",
			before: []int64{20, 30},
			after:  []int64{30},
			want:   []sessionCall{{"revoke", 20}},
		},
		{
			name:   "replacement",
			before: []int64{20},
			after:  []int64{30},
			want: []sessionCall{
				{"revoke", 20},
				{"rotate", 30},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &sessionManagerSpy{}
			changes := membershipSessionChanges(tt.before, tt.after)

			if err := changes.Apply(context.Background(), spy); err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(spy.calls, tt.want) {
				t.Fatalf("calls = %v, want %v", spy.calls, tt.want)
			}
		})
	}
}

func TestAccessSessionChanges(t *testing.T) {
	tests := []struct {
		name   string
		before []groupAccessEntry
		after  []groupAccessEntry
		want   []sessionCall
	}{
		{
			name:   "unchanged",
			before: []groupAccessEntry{{DatabaseID: 10, Level: "view"}},
			after:  []groupAccessEntry{{DatabaseID: 10, Level: "view"}},
		},
		{
			name:  "new access rotates",
			after: []groupAccessEntry{{DatabaseID: 10, Level: "view"}},
			want:  []sessionCall{{"rotate", 20}},
		},
		{
			name:   "upgrade rotates",
			before: []groupAccessEntry{{DatabaseID: 10, Level: "view"}},
			after:  []groupAccessEntry{{DatabaseID: 10, Level: "edit"}},
			want:   []sessionCall{{"rotate", 20}},
		},
		{
			name:   "downgrade revokes",
			before: []groupAccessEntry{{DatabaseID: 10, Level: "edit"}},
			after:  []groupAccessEntry{{DatabaseID: 10, Level: "view"}},
			want:   []sessionCall{{"revoke", 20}},
		},
		{
			name:   "removal revokes",
			before: []groupAccessEntry{{DatabaseID: 10, Level: "view"}},
			want:   []sessionCall{{"revoke", 20}},
		},
		{
			name:   "mixed change prioritizes revocation",
			before: []groupAccessEntry{{DatabaseID: 10, Level: "view"}},
			after:  []groupAccessEntry{{DatabaseID: 11, Level: "edit"}},
			want:   []sessionCall{{"revoke", 20}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &sessionManagerSpy{}
			changes := accessSessionChanges(
				[]int64{20}, tt.before, tt.after,
			)

			if err := changes.Apply(context.Background(), spy); err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(spy.calls, tt.want) {
				t.Fatalf("calls = %v, want %v", spy.calls, tt.want)
			}
		})
	}
}

func TestSessionChangesPrioritizeRevoke(t *testing.T) {
	changes := newSessionChanges()
	changes.Rotate(20)
	changes.Revoke(20)
	changes.Rotate(20)

	spy := &sessionManagerSpy{}
	if err := changes.Apply(context.Background(), spy); err != nil {
		t.Fatal(err)
	}

	want := []sessionCall{{"revoke", 20}}
	if !reflect.DeepEqual(spy.calls, want) {
		t.Fatalf("calls = %v, want %v", spy.calls, want)
	}
}

func TestSessionChangesStopOnFailure(t *testing.T) {
	expected := errors.New("session update failed")
	spy := &sessionManagerSpy{err: expected}

	changes := newSessionChanges()
	changes.Revoke(20)
	changes.Rotate(30)

	err := changes.Apply(context.Background(), spy)
	if !errors.Is(err, expected) {
		t.Fatalf("error = %v, want %v", err, expected)
	}
	if len(spy.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(spy.calls))
	}
}