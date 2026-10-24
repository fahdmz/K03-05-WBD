package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthenticated    = errors.New("unauthenticated")
)

type Session struct {
	ID        int64
	User      User
	CSRFToken string
	NewToken  string
}

type Service struct {
	pool *pgxpool.Pool
	cfg  Config
	now  func() time.Time
}

var svc *Service

func (s *Service) RevokeUserSessions(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sessions SET revoked_at = $1
		WHERE user_id = $2 AND revoked_at IS NULL
	`,
		s.now(), userID)

	return err
}

func (s *Service) RotateUserSessions(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sessions SET last_rotated_at = 'epoch'::timestamptz
		WHERE user_id = $1 AND revoked_at IS NULL
	`, userID)
	return err
}

var _ SessionManager = (*Service)(nil)

func NewService(pool *pgxpool.Pool, cfg Config) *Service {
	return &Service{pool: pool, cfg: cfg, now: time.Now}
}

func Init(pool *pgxpool.Pool) *Service {
	svc = NewService(pool, DefaultConfig())
	return svc
}

func (s *Service) EnsureAdmin(ctx context.Context, username, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO users (username, password_hash, is_admin)
		VALUES ($1, $2, TRUE)
		ON CONFLICT (username) DO NOTHING
	`, username, hash)
	return err
}

func (s *Service) Login(ctx context.Context, username string, password string) (string, *Session, error) {
	var (
		u      User
		hash   string
		active bool
	)

	err := s.pool.QueryRow(ctx,
		`SELECT id, username, password_hash, is_admin, is_active FROM users WHERE username = $1`,
		username,
	).Scan(&u.ID, &u.Username, &hash, &u.IsAdmin, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrInvalidCredentials
	}

	if err != nil {
		return "", nil, err
	}

	if !active || !CheckPassword(hash, password) {
		return "", nil, ErrInvalidCredentials
	}

	token, err := newToken()

	if err != nil {
		return "", nil, err
	}

	csrf, err := newToken()
	if err != nil {
		return "", nil, err
	}

	now := s.now()
	var id int64
	err = s.pool.QueryRow(ctx, `
		INSERT INTO sessions (user_id, current_token_hash, csrf_token,
		                      created_at, last_activity_at, last_rotated_at, absolute_expires_at)
		VALUES ($1, $2, $3, $4, $4, $4, $5)
		RETURNING id`,
		u.ID, hashToken(token), csrf, now, now.Add(s.cfg.AbsoluteTimeout),
	).Scan(&id)

	if err != nil {
		return "", nil, err
	}

	return token, &Session{ID: id, User: u, CSRFToken: csrf}, nil
}

const authQuery = `
SELECT s.id, s.csrf_token, s.last_rotated_at,
       (s.current_token_hash = $1) AS is_current,
       u.id, u.username, u.is_admin
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE (s.current_token_hash = $1
       OR (s.previous_token_hash = $1 AND s.previous_valid_until > $2))
  AND s.revoked_at IS NULL
  AND s.absolute_expires_at > $2
  AND s.last_activity_at > $3
  AND u.is_active`

func (s *Service) rotate(ctx context.Context, sessionID int64, oldHash string) (string, error) {
	fresh, err := newToken()
	if err != nil {
		return "", err
	}

	now := s.now()
	tag, err := s.pool.Exec(ctx, `
			UPDATE sessions
		SET previous_token_hash  = current_token_hash,
		    previous_valid_until = $1,
		    current_token_hash   = $2,
		    last_rotated_at      = $3,
		    last_activity_at     = $3
		WHERE id = $4 AND current_token_hash = $5`,
		now.Add(s.cfg.GracePeriod), hashToken(fresh), now, sessionID, oldHash)

	if err != nil {
		return "", err
	}

	if tag.RowsAffected() == 0 {
		return "", nil
	}
	return fresh, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (*Session, error) {
	now := s.now()
	h := hashToken(token)

	var (
		sess      Session
		rotatedAt time.Time
		isCurrent bool
	)

	err := s.pool.QueryRow(ctx, authQuery, h, now, now.Add(-s.cfg.IdleTimeout)).Scan(
		&sess.ID, &sess.CSRFToken, &rotatedAt, &isCurrent,
		&sess.User.ID, &sess.User.Username, &sess.User.IsAdmin,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnauthenticated
	}

	if err != nil {
		return nil, err
	}

	if isCurrent && now.Sub(rotatedAt) >= s.cfg.RotateInterval {
		fresh, rerr := s.rotate(ctx, sess.ID, h)
		if rerr != nil {
			return nil, err
		}
		sess.NewToken = fresh
		return &sess, nil
	}

	_, err = s.pool.Exec(ctx,
		`UPDATE sessions SET last_activity_at = $1 WHERE id = $2`, now, sess.ID)

	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sessions SET revoked_at = $1
		WHERE (current_token_hash = $2 OR previous_token_hash = $2)
		  AND revoked_at IS NULL`,
		s.now(), hashToken(token))
	return err
}
