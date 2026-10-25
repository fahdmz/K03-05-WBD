package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func absoluteExpiry(t *testing.T, s *Service, sessionID int64) time.Time {
	t.Helper()
	var v time.Time
	err := s.pool.QueryRow(context.Background(),
		`SELECT absolute_expires_at FROM sessions WHERE id = $1`, sessionID).Scan(&v)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestLoginBerhasil(t *testing.T) {
	s, _ := newTestService(t)
	createUser(t, s, "budi", "rahasia123", true)

	token, sess, err := s.Login(context.Background(), "budi", "rahasia123")
	if err != nil {
		t.Fatalf("login gagal: %v", err)
	}
	if sess.User.Username != "budi" || sess.CSRFToken == "" {
		t.Fatalf("session tidak lengkap: %+v", sess)
	}

	var stored string
	err = s.pool.QueryRow(context.Background(),
		`SELECT current_token_hash FROM sessions WHERE id = $1`, sess.ID).Scan(&stored)
	if err != nil {
		t.Fatal(err)
	}
	if stored == token {
		t.Fatal("token disimpan dalam bentuk asli, seharusnya hash")
	}
	if stored != hashToken(token) {
		t.Fatal("hash token di database tidak cocok")
	}
}

func TestLoginGagal(t *testing.T) {
	s, _ := newTestService(t)
	createUser(t, s, "budi", "rahasia123", true)
	createUser(t, s, "nonaktif", "rahasia123", false)

	cases := []struct {
		name, username, password string
	}{
		{"password salah", "budi", "salah"},
		{"user tidak ada", "siapa", "rahasia123"},
		{"user nonaktif", "nonaktif", "rahasia123"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := s.Login(context.Background(), c.username, c.password)
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("harusnya ErrInvalidCredentials, dapat %v", err)
			}
		})
	}
}

func TestIdleTimeout(t *testing.T) {
	s, clock := newTestService(t)
	createUser(t, s, "budi", "rahasia123", true)
	token := mustLogin(t, s, "budi", "rahasia123")

	clock.Advance(29 * time.Minute)
	token, err := touch(s, token)
	if err != nil {
		t.Fatalf("baru 29 menit idle, harusnya masih valid: %v", err)
	}

	clock.Advance(31 * time.Minute)
	if _, err := touch(s, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("idle 31 menit, harusnya ditolak, dapat %v", err)
	}
}

func TestAbsoluteExpiration(t *testing.T) {
	s, clock := newTestService(t)
	createUser(t, s, "budi", "rahasia123", true)
	token := mustLogin(t, s, "budi", "rahasia123")

	var err error
	for elapsed := 10 * time.Minute; elapsed < 8*time.Hour; elapsed += 10 * time.Minute {
		clock.Advance(10 * time.Minute)
		token, err = touch(s, token)
		if err != nil {
			t.Fatalf("aktif dan baru %v, harusnya valid: %v", elapsed, err)
		}
	}

	clock.Advance(10 * time.Minute)
	if _, err := touch(s, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("sudah 8 jam, harusnya ditolak walaupun aktif, dapat %v", err)
	}
}

func TestRotasiDanGracePeriod(t *testing.T) {
	s, clock := newTestService(t)
	createUser(t, s, "budi", "rahasia123", true)
	ctx := context.Background()

	oldToken, sess, err := s.Login(ctx, "budi", "rahasia123")
	if err != nil {
		t.Fatal(err)
	}
	expiresBefore := absoluteExpiry(t, s, sess.ID)

	clock.Advance(5 * time.Minute)
	got, err := s.Authenticate(ctx, oldToken)
	if err != nil {
		t.Fatal(err)
	}
	if got.NewToken != "" {
		t.Fatal("belum 15 menit, seharusnya belum dirotasi")
	}

	clock.Advance(11 * time.Minute)
	got, err = s.Authenticate(ctx, oldToken)
	if err != nil {
		t.Fatal(err)
	}
	if got.NewToken == "" || got.NewToken == oldToken {
		t.Fatal("sudah 16 menit, seharusnya dirotasi dan mendapat token baru")
	}
	newToken := got.NewToken

	clock.Advance(10 * time.Second)
	if _, err := s.Authenticate(ctx, oldToken); err != nil {
		t.Fatalf("token lama masih dalam grace period, harusnya valid: %v", err)
	}

	clock.Advance(25 * time.Second)
	if _, err := s.Authenticate(ctx, oldToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("grace period habis, token lama harusnya ditolak, dapat %v", err)
	}
	if _, err := s.Authenticate(ctx, newToken); err != nil {
		t.Fatalf("token baru harusnya valid: %v", err)
	}

	if !absoluteExpiry(t, s, sess.ID).Equal(expiresBefore) {
		t.Fatal("rotasi tidak boleh mengubah absolute expiration")
	}
}

func TestLogout(t *testing.T) {
	s, _ := newTestService(t)
	createUser(t, s, "budi", "rahasia123", true)
	token := mustLogin(t, s, "budi", "rahasia123")
	ctx := context.Background()

	if err := s.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("setelah logout harusnya ditolak, dapat %v", err)
	}
}

func TestRevokeUserSessions(t *testing.T) {
	s, _ := newTestService(t)
	budi := createUser(t, s, "budi", "rahasia123", true)
	createUser(t, s, "ani", "rahasia123", true)
	ctx := context.Background()

	t1 := mustLogin(t, s, "budi", "rahasia123")
	t2 := mustLogin(t, s, "budi", "rahasia123")
	t3 := mustLogin(t, s, "ani", "rahasia123")

	if err := s.RevokeUserSessions(ctx, budi); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{t1, t2} {
		if _, err := s.Authenticate(ctx, tok); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("session budi harusnya sudah dicabut, dapat %v", err)
		}
	}
	if _, err := s.Authenticate(ctx, t3); err != nil {
		t.Fatalf("session user lain tidak boleh ikut dicabut: %v", err)
	}
}

func TestRotateUserSessions(t *testing.T) {
	s, clock := newTestService(t)
	id := createUser(t, s, "budi", "rahasia123", true)
	token := mustLogin(t, s, "budi", "rahasia123")
	ctx := context.Background()

	clock.Advance(time.Minute)
	if err := s.RotateUserSessions(ctx, id); err != nil {
		t.Fatal(err)
	}
	got, err := s.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if got.NewToken == "" {
		t.Fatal("setelah privilege naik, request berikutnya harusnya langsung dirotasi")
	}
}

func TestUserDinonaktifkanSessionDitolak(t *testing.T) {
	s, _ := newTestService(t)
	id := createUser(t, s, "budi", "rahasia123", true)
	token := mustLogin(t, s, "budi", "rahasia123")
	ctx := context.Background()

	if _, err := s.pool.Exec(ctx, `UPDATE users SET is_active = FALSE WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("user nonaktif harusnya ditolak, dapat %v", err)
	}
}

func TestInTxIkutTransaksi(t *testing.T) {
	s, _ := newTestService(t)
	budi := createUser(t, s, "budi", "rahasia123", true)
	ani := createUser(t, s, "ani", "rahasia123", true)
	tokenBudi := mustLogin(t, s, "budi", "rahasia123")
	tokenAni := mustLogin(t, s, "ani", "rahasia123")
	ctx := context.Background()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InTx(tx).RevokeUserSessions(ctx, budi); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, tokenBudi); err != nil {
		t.Fatalf("transaksi di-rollback, session harusnya tetap valid: %v", err)
	}

	tx, err = s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := s.InTx(tx)
	if err := m.RevokeUserSessions(ctx, budi); err != nil {
		t.Fatal(err)
	}
	if err := m.RotateUserSessions(ctx, ani); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Authenticate(ctx, tokenBudi); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("setelah commit, session budi harusnya dicabut, dapat %v", err)
	}
	got, err := s.Authenticate(ctx, tokenAni)
	if err != nil {
		t.Fatal(err)
	}
	if got.NewToken == "" {
		t.Fatal("setelah commit, session ani harusnya dirotasi")
	}
}
