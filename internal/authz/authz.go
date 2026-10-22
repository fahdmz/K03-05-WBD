package authz

import (
	"context"
	"errors"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
)

type Level int

const (
	None Level = 0
	View Level = 1
	Edit Level = 2
)

var ErrForbidden = errors.New("forbidden")

type Authorizer interface {
	EffectiveLevel(ctx context.Context, userID, dbID int64) (Level, error)
	Require(ctx context.Context, userID, dbID int64, min Level) error
}

func RequireAdmin(u *auth.User) error {
	if u == nil || !u.IsAdmin {
		return ErrForbidden
	}
	return nil
}

type AllowAll struct{}

func (AllowAll) EffectiveLevel(ctx context.Context, userID, dbID int64) (Level, error) {
	return Edit, nil
}

func (AllowAll) Require(ctx context.Context, userID, dbID int64, min Level) error {
	return nil
}
