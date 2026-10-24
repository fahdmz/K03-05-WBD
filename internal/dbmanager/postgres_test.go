package dbmanager

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDecryptPassword(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	plain := []byte("target-db-secret")
	sealed := append(nonce, gcm.Seal(nil, nonce, plain, nil)...)

	got, err := decryptPassword(key, sealed)
	if err != nil {
		t.Fatalf("decryptPassword() error = %v", err)
	}
	if got != string(plain) {
		t.Fatalf("decryptPassword() = %q, want %q", got, plain)
	}
}

func TestDecryptPasswordRejectsTruncatedData(t *testing.T) {
	_, err := decryptPassword(make([]byte, 32), []byte("short"))
	if err == nil {
		t.Fatal("decryptPassword() expected an error for truncated ciphertext")
	}
}

func TestTargetPoolConfigQuotesCredentials(t *testing.T) {
	cfg, err := targetPoolConfig(ConnConfig{
		Host: "localhost", Port: 5433, DBName: "shop db", Username: "user@example.com",
		Password: "p@ss:/word?", SSLMode: "disable",
	})
	if err != nil {
		t.Fatalf("targetPoolConfig() error = %v", err)
	}
	if cfg.ConnConfig.Password != "p@ss:/word?" {
		t.Fatalf("password parsed as %q", cfg.ConnConfig.Password)
	}
	if cfg.ConnConfig.Database != "shop db" {
		t.Fatalf("database parsed as %q", cfg.ConnConfig.Database)
	}
}

func TestNewPostgresManagerFromEnvValidatesKey(t *testing.T) {
	t.Setenv(EncryptionKeyEnv, "")
	if _, err := NewPostgresManagerFromEnv(nil); err == nil {
		t.Fatal("expected missing encryption key to be rejected")
	}

	t.Setenv(EncryptionKeyEnv, "not-base64!")
	if _, err := NewPostgresManagerFromEnv(nil); err == nil {
		t.Fatal("expected malformed Base64 key to be rejected")
	}

	key := make([]byte, 32)
	t.Setenv(EncryptionKeyEnv, base64.StdEncoding.EncodeToString(key))
	if _, err := NewPostgresManagerFromEnv(nil); err == nil {
		t.Fatal("expected nil internal pool to be rejected after parsing a valid key")
	}
}

func TestConnectionTestErrorDoesNotExposePassword(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager := &PostgresManager{}
	err := manager.Test(ctx, ConnConfig{
		Host: "localhost", Port: 5432, DBName: "platform", Username: "app",
		Password: "sensitive-password", SSLMode: "disable",
	})
	if err == nil {
		t.Fatal("expected canceled connection test to fail")
	}
	if strings.Contains(err.Error(), "sensitive-password") {
		t.Fatal("connection test error exposed the password")
	}
}

func TestPostgresManagerIntegration(t *testing.T) {
	platformURL := envOr("POSTGRES_MANAGER_TEST_DATABASE_URL", testDatabaseURL())
	targetURL := envOr("POSTGRES_MANAGER_TEST_TARGET_URL", testDatabaseURL())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	platform := setupPlatformDB(t, platformURL)
	setupTargetDB(t, targetURL)
	targetConfig, err := pgxpool.ParseConfig(targetURL)
	if err != nil {
		t.Fatalf("parse target test database URL: %v", err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generate encryption key: %v", err)
	}
	encryptedPassword, err := encryptTestPassword(key, targetConfig.ConnConfig.Password)
	if err != nil {
		t.Fatalf("encrypt target password: %v", err)
	}

	name := fmt.Sprintf("manager_test_%d", time.Now().UnixNano())
	var dbID int64
	if err := platform.QueryRow(ctx, `
		INSERT INTO db_connections (name, host, port, dbname, username, password_encrypted, sslmode)
		VALUES ($1, $3, $4, $5, $6, $2, $7)
		RETURNING id`, name, encryptedPassword,
		targetConfig.ConnConfig.Host,
		targetConfig.ConnConfig.Port,
		targetConfig.ConnConfig.Database,
		targetConfig.ConnConfig.User,
		targetConfig.ConnConfig.RuntimeParams["sslmode"],
	).Scan(&dbID); err != nil {
		t.Fatalf("create database connection fixture: %v", err)
	}

	manager, err := NewPostgresManager(platform, key)
	if err != nil {
		t.Fatalf("create postgres manager: %v", err)
	}
	t.Cleanup(func() { manager.Invalidate(dbID) })
	first, err := manager.Pool(ctx, dbID)
	if err != nil {
		t.Fatalf("open target pool through manager: %v", err)
	}
	var result int
	if err := first.QueryRow(ctx, "SELECT 1").Scan(&result); err != nil || result != 1 {
		t.Fatalf("query through managed pool = %d, err=%v", result, err)
	}
	second, err := manager.Pool(ctx, dbID)
	if err != nil {
		t.Fatalf("get cached target pool: %v", err)
	}
	if first != second {
		t.Fatal("manager did not return the cached pool")
	}

	manager.Invalidate(dbID)
	third, err := manager.Pool(ctx, dbID)
	if err != nil {
		t.Fatalf("reopen target pool after invalidation: %v", err)
	}
	if third == first {
		t.Fatal("manager returned the invalidated pool")
	}
}

func encryptTestPassword(key []byte, password string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, gcm.Seal(nil, nonce, []byte(password), nil)...), nil
}
