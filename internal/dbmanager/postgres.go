package dbmanager

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrDatabaseNotFound = errors.New("database connection not found")

const EncryptionKeyEnv = "DB_ENCRYPTION_KEY"

// PostgresManager buat buka dan nyimpen pool koneksi database target.
// password_encrypted isinya nonce, ciphertext, sama tag dari AES-GCM.
type PostgresManager struct {
	internal *pgxpool.Pool
	key      []byte

	mu         sync.Mutex
	pools      map[int64]*pgxpool.Pool
	generation map[int64]uint64
}

// NewPostgresManagerFromEnv ambil key AES 32 byte dari DB_ENCRYPTION_KEY.
func NewPostgresManagerFromEnv(internal *pgxpool.Pool) (*PostgresManager, error) {
	encoded := strings.TrimSpace(os.Getenv(EncryptionKeyEnv))
	if encoded == "" {
		return nil, fmt.Errorf("%s is required", EncryptionKeyEnv)
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%s must contain a Base64-encoded AES-256 key", EncryptionKeyEnv)
	}
	return NewPostgresManager(internal, key)
}

// NewPostgresManager bikin manager baru. Key-nya wajib 32 byte biar cocok buat AES-256.
func NewPostgresManager(internal *pgxpool.Pool, key []byte) (*PostgresManager, error) {
	if internal == nil {
		return nil, errors.New("internal database pool is required")
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("database encryption key must be 32 bytes, got %d", len(key))
	}
	return &PostgresManager{
		internal:   internal,
		key:        append([]byte(nil), key...),
		pools:      make(map[int64]*pgxpool.Pool),
		generation: make(map[int64]uint64),
	}, nil
}

var _ Manager = (*PostgresManager)(nil)

func (m *PostgresManager) Pool(ctx context.Context, dbID int64) (*pgxpool.Pool, error) {
	for {
		m.mu.Lock()
		if cached := m.pools[dbID]; cached != nil {
			m.mu.Unlock()
			return cached, nil
		}
		generation := m.generation[dbID]
		m.mu.Unlock()

		cfg, encryptedPassword, err := m.connectionConfig(ctx, dbID)
		if err != nil {
			return nil, err
		}
		password, err := decryptPassword(m.key, encryptedPassword)
		if err != nil {
			return nil, fmt.Errorf("decrypt database connection %d password: %w", dbID, err)
		}
		cfg.Password = password

		pool, err := openPool(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("connect to target database %d: %w", dbID, err)
		}

		m.mu.Lock()
		if existing := m.pools[dbID]; existing != nil {
			m.mu.Unlock()
			pool.Close()
			return existing, nil
		}
		if generation != m.generation[dbID] {
			m.mu.Unlock()
			pool.Close()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			continue
		}
		m.pools[dbID] = pool
		m.mu.Unlock()
		return pool, nil
	}
}

func (m *PostgresManager) Test(ctx context.Context, cfg ConnConfig) error {
	parsed, err := targetPoolConfig(cfg)
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, parsed)
	if err != nil {
		return errors.New("target database connection failed")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return errors.New("target database connection failed")
	}
	return nil
}

func (m *PostgresManager) Invalidate(dbID int64) {
	m.mu.Lock()
	pool := m.pools[dbID]
	delete(m.pools, dbID)
	m.generation[dbID]++
	m.mu.Unlock()
	if pool != nil {
		pool.Close()
	}
}

func (m *PostgresManager) connectionConfig(ctx context.Context, dbID int64) (ConnConfig, []byte, error) {
	var cfg ConnConfig
	var encrypted []byte
	err := m.internal.QueryRow(ctx, `
		SELECT host, port, dbname, username, password_encrypted, sslmode
		FROM db_connections
		WHERE id = $1`, dbID,
	).Scan(&cfg.Host, &cfg.Port, &cfg.DBName, &cfg.Username, &encrypted, &cfg.SSLMode)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConnConfig{}, nil, ErrDatabaseNotFound
	}
	if err != nil {
		return ConnConfig{}, nil, fmt.Errorf("load database connection %d: %w", dbID, err)
	}
	return cfg, encrypted, nil
}

func decryptPassword(key []byte, encrypted []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(encrypted) < gcm.NonceSize()+gcm.Overhead() {
		return "", errors.New("encrypted password is truncated")
	}
	nonce := encrypted[:gcm.NonceSize()]
	ciphertext := encrypted[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", errors.New("encrypted password could not be authenticated")
	}
	return string(plaintext), nil
}

func openPool(ctx context.Context, cfg ConnConfig) (*pgxpool.Pool, error) {
	parsed, err := targetPoolConfig(cfg)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, parsed)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func targetPoolConfig(cfg ConnConfig) (*pgxpool.Config, error) {
	if strings.TrimSpace(cfg.Host) == "" || strings.TrimSpace(cfg.DBName) == "" || strings.TrimSpace(cfg.Username) == "" {
		return nil, errors.New("host, database name, and username are required")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("invalid database port %d", cfg.Port)
	}
	sslMode := cfg.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	if !validSSLMode(sslMode) {
		return nil, fmt.Errorf("invalid PostgreSQL sslmode %q", sslMode)
	}

	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.Username, cfg.Password),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:   "/" + cfg.DBName,
	}
	query := url.Values{}
	query.Set("sslmode", sslMode)
	u.RawQuery = query.Encode()
	parsed, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		return nil, errors.New("invalid target database connection settings")
	}
	parsed.MaxConns = 5
	return parsed, nil
}

func validSSLMode(mode string) bool {
	switch mode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		return true
	default:
		return false
	}
}
