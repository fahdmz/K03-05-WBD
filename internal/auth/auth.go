package auth

import (
	"context"
	"net/http"
)

type User struct {
	ID       int64
	Username string
	IsAdmin  bool
}

type contextKey struct{}

func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, contextKey{}, u)
}

func UserFromContext(ctx context.Context) (*User, bool) {
	u, ok := ctx.Value(contextKey{}).(*User)
	return u, ok
}

func RequireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		svc.Protect(next).ServeHTTP(w, r)
	})
}

type SessionManager interface {
	RevokeUserSessions(ctx context.Context, userID int64) error
	RotateUserSessions(ctx context.Context, userID int64) error
}

type NoopSessionManager struct{}

func (NoopSessionManager) RevokeUserSessions(ctx context.Context, userID int64) error {
	return nil
}

func (NoopSessionManager) RotateUserSessions(ctx context.Context, userID int64) error {
	return nil
}
