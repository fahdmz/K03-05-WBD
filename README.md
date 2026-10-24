# K03-05-WBD: Database Management Platform

Tugas Besar IF3110 Pengembangan Aplikasi Web 2026, Milestone 1.

Platform web untuk mengatur akses ke database PostgreSQL bersama: login, pengelolaan user dan group, database viewer, serta migration yang tercatat dan dijalankan di dalam transaction.

## State

Sudah selesai:

1. Struktur project Go dan Docker Compose berisi 3 PostgreSQL (internal, target contoh, testing)
2. Schema database internal (`migrations/001_init.sql`)
3. Autentikasi dan session: login, logout, idle timeout, absolute expiration, rotasi session ID, dan perlindungan CSRF, beserta test otomatisnya
4. Fondasi frontend: API client, router dan guard, layout, halaman login, komponen bersama, dan notifikasi

Sedang dikerjakan: authorization dan halaman admin, database viewer, dan fitur migration.

## Prasyarat

1. Go 1.22 atau lebih baru
2. Node.js (LTS)
3. Docker Desktop
4. Git

## Menjalankan secara lokal

1. Copy file environment, lalu isi `ENCRYPTION_KEY` dengan hasil `openssl rand -base64 32`:
   ```
   cp .env.example .env
   ```
2. Jalankan database:
   ```
   docker compose up -d
   ```
3. Buat tabel internal:
   ```
   docker compose exec -T platform-db psql -U platform -d platform < migrations/001_init.sql
   ```
4. Jalankan backend:
   ```
   go run ./cmd/server
   ```
   Cek http://localhost:8080/api/health, harus menampilkan `ok`.
5. Jalankan frontend di terminal lain:
   ```
   cd frontend
   npm install
   npm run dev
   ```
6. Buka http://localhost:5173 dan login dengan akun admin awal dari `.env` (default `admin` / `admin123`). Akun ini dibuat otomatis saat server pertama kali dijalankan.

Untuk mereset database dari nol:

```
docker compose down -v
docker compose up -d
```

lalu ulangi langkah 3 dan jalankan ulang server.

## Database lokal

1. `platform-db` di port 5435: database internal aplikasi
2. `target-db` di port 5433: contoh database target untuk demo
3. `test-db` di port 5434: khusus integration test, data hilang saat restart

## Environment variable

1. `DATABASE_URL`: alamat database internal
2. `ADMIN_USERNAME`, `ADMIN_PASSWORD`: akun Platform Admin awal, hanya dibuat kalau username tersebut belum ada
3. `APP_ENV`: `development` atau `production`. Di `production`, cookie session diberi flag Secure.
4. `ENCRYPTION_KEY`: key AES-256 dalam Base64 untuk mengenkripsi password database target

## Autentikasi dan session

Login memakai username dan password. Password disimpan sebagai hash bcrypt. Setelah login berhasil, server membuat session ID acak (32 byte dari `crypto/rand`) dan mengirimkannya lewat cookie `session_id` dengan flag HttpOnly, SameSite=Lax, dan Secure (saat `APP_ENV=production`). Database hanya menyimpan hash SHA-256 dari session ID, bukan nilai aslinya. Session ID tidak pernah muncul di URL, body response, maupun log.

Nilai yang dipakai (diatur di `internal/auth/config.go`):

1. **Idle timeout: 30 menit.** Session berakhir kalau tidak ada request selama 30 menit. Cukup longgar untuk membaca schema atau menyusun migration tanpa terputus, tetapi tetap membatasi risiko session yang ditinggal terbuka.
2. **Absolute expiration: 8 jam.** Session berakhir 8 jam setelah login walaupun user terus aktif, kira-kira satu hari kerja. User wajib login ulang setiap hari.
3. **Interval rotasi: 15 menit.** Session ID diganti pada request pertama setelah 15 menit sejak rotasi terakhir, sehingga ID yang bocor hanya berguna dalam waktu singkat. Rotasi juga terjadi saat login (selalu session baru, dan session lama yang dibawa browser dicabut) dan saat privilege user naik (request berikutnya dari user tersebut langsung dirotasi).
4. **Grace period: 30 detik.** Setelah rotasi, ID lama masih diterima selama 30 detik untuk menangani request yang berjalan bersamaan atau response yang tidak sampai ke browser. Setelah itu ID lama ditolak.

Rotasi tidak pernah mengubah waktu absolute expiration.

Logout dan pencabutan akses menandai session sebagai dicabut di database (`revoked_at`), jadi session ID yang sama tidak bisa dipakai lagi walaupun masih tersimpan di browser. User yang dinonaktifkan langsung kehilangan semua session-nya.

Setiap request selain GET, HEAD, dan OPTIONS wajib membawa header `X-CSRF-Token` yang nilainya didapat dari response login atau `GET /api/auth/me`. Request tanpa token atau dengan token salah ditolak dengan status 403.

## Menjalankan test

Pastikan `test-db` berjalan (`docker compose up -d`), lalu:

```
go test ./... -v
```

Integration test memakai `test-db` (port 5434). Setiap package membuat schema sendiri (misalnya `auth_test`) dan membuat ulang tabelnya di setiap test, sehingga test bisa dijalankan berulang dan tidak saling mengganggu. Kalau `test-db` tidak berjalan, integration test dilewati dengan pesan yang jelas.

## Struktur repo

```
cmd/server/            entry point, menyambungkan semua bagian
internal/auth/         login, session, middleware RequireLogin
internal/authz/        pengecekan hak akses
internal/admin/        endpoint admin user, group, koneksi database
internal/dbmanager/    koneksi ke database target
internal/migration/    viewer dan migration
internal/store/        koneksi ke database internal
internal/web/          helper JSON bersama
migrations/            schema database internal
frontend/src/lib/        API client, auth context, notifikasi
frontend/src/components/ layout, guard, komponen UI bersama
frontend/src/features/   halaman per fitur (auth, admin, databases)
```

## Anggota

1. Fahd Muhammad Zahid - 13524078
2. Nathanael Gunawan - 13524066
3. Mahatma Brahmana - 13524015