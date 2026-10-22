package dbmanager

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

type ConnConfig struct {
	Host     string
	Port     int
	DBName   string
	Username string
	Password string
	SSLMode  string
}

type Manager interface {
	Pool(ctx context.Context, dbID int64) (*pgxpool.Pool, error)
	Test(ctx context.Context, cfg ConnConfig) error
	Invalidate(dbID int64)
}

var ErrNotImplemented = errors.New("not implemented yet")

type Stub struct{}

func (Stub) Pool(ctx context.Context, dbID int64) (*pgxpool.Pool, error) {
	return nil, ErrNotImplemented
}

func (Stub) Test(ctx context.Context, cfg ConnConfig) error {
	return nil
}

func (Stub) Invalidate(dbID int64) {}
