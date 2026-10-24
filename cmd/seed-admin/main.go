package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fahdmz/K03-05-WBD/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println("Admin berhasil dibuat.")
}

func run() error {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return errors.New("gagal membaca .env")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	username := strings.TrimSpace(os.Getenv("ADMIN_USERNAME"))
	password := os.Getenv("ADMIN_PASSWORD")

	if databaseURL == "" {
		return errors.New("DATABASE_URL wajib diatur")
	}

	if username == "" ||
		utf8.RuneCountInString(username) > 100 ||
		strings.ContainsRune(username, '\x00') {
		return errors.New("ADMIN_USERNAME harus berisi 1–100 karakter tanpa NUL")
	}

	if utf8.RuneCountInString(password) < 8 || len(password) > 72 {
		return errors.New("ADMIN_PASSWORD minimal 8 karakter dan maksimal 72 byte")
	}

	hash, err := bcrypt.GenerateFromPassword(
		[]byte(password), bcrypt.DefaultCost,
	)
	if err != nil {
		return errors.New("gagal memproses password")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	db, err := store.Connect(ctx, databaseURL)
	if err != nil {
		return errors.New("gagal terhubung ke database internal")
	}
	defer db.Close()

	_, err = db.Exec(ctx, `
		INSERT INTO users (
			username, password_hash, is_admin, is_active
		)
		VALUES ($1, $2, TRUE, TRUE)
	`, username, string(hash))

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) &&
			pgErr.Code == "23505" &&
			pgErr.ConstraintName == "users_username_key" {
			return errors.New("username sudah digunakan; akun lama tidak diubah")
		}

		return errors.New("gagal membuat admin; periksa schema dan koneksi database")
	}

	return nil
}