# K03-05-WBD: Database Management Platform

Tugas Besar IF3110 Pengembangan Aplikasi Web 2026, Milestone 1.

Platform web untuk mengatur akses ke database PostgreSQL bersama. Platform Admin mengelola user, group, koneksi database, dan hak akses. User dapat menjelajahi schema dan data database yang boleh diaksesnya, lalu menyusun dan menjalankan migration yang tercatat secara linear dan dijalankan di dalam transaction.

Backend: Go (API JSON di `/api`). Frontend: React + Vite + Tailwind di `frontend/`. Database: PostgreSQL lewat Docker Compose.

## Prasyarat

1. Go sesuai versi di `go.mod`
2. Node.js LTS
3. Docker
4. Git

## Menjalankan secara lokal

Dari root repository:

1. Siapkan environment, lalu isi `DB_ENCRYPTION_KEY` di `.env` dengan hasil `openssl rand -base64 32`:
   ```
   cp .env.example .env
   openssl rand -base64 32
   ```
2. Jalankan database:
   ```
   docker compose up -d
   ```
3. Terapkan schema internal (sekali untuk volume baru):
   ```
   docker compose exec -T platform-db psql -v ON_ERROR_STOP=1 -U platform -d platform < migrations/001_init.sql
   ```
4. Jalankan backend (http://localhost:8080):
   ```
   go run ./cmd/server
   ```
5. Jalankan frontend di terminal lain:
   ```
   cd frontend
   npm install
   npm run dev
   ```
6. Buka http://localhost:5173 dan login sebagai admin (default `admin` / `admin123`, dibuat otomatis saat server start).
7. Di halaman Admin, tambahkan koneksi ke database contoh: host `localhost`, port `5433`, user `target`, password `target`, database `shop`, SSL mode `disable`. Lalu buat group, masukkan user, dan beri group akses View atau Edit.

Reset semua data: `docker compose down -v`, `docker compose up -d`, lalu ulangi langkah 3.

## Environment variable

1. `DATABASE_URL`: alamat database internal.
2. `ADMIN_USERNAME`, `ADMIN_PASSWORD`: akun Platform Admin awal, hanya dibuat kalau username belum ada.
3. `APP_ENV`: `development` atau `production`. Di `production`, cookie session diberi flag Secure.
4. `DB_ENCRYPTION_KEY`: key AES-256 (Base64 dari 32 byte) untuk mengenkripsi password koneksi database target. Jangan diganti setelah ada koneksi tersimpan.

Database lokal: `platform-db` (5435, internal), `target-db` (5433, contoh untuk demo), `test-db` (5434, khusus test).

## Autentikasi dan session

Password disimpan sebagai hash bcrypt. Setelah login, server membuat session ID acak 32 byte yang dikirim lewat cookie `session_id` (HttpOnly, SameSite=Lax, Secure di production). Database hanya menyimpan hash SHA-256-nya. Session ID tidak muncul di URL, response, maupun log.

Nilai yang dipakai (`internal/auth/config.go`):

1. **Idle timeout 30 menit**: cukup longgar untuk membaca schema atau menyusun migration, tetapi membatasi risiko session yang ditinggal terbuka.
2. **Absolute expiration 8 jam**: kira-kira satu hari kerja, setelah itu wajib login ulang walaupun masih aktif.
3. **Rotasi setiap 15 menit**: session ID diganti pada request berikutnya, sehingga ID yang bocor cepat tidak berlaku. Rotasi juga terjadi saat login dan saat privilege naik.
4. **Grace period 30 detik**: ID lama masih diterima sebentar setelah rotasi, untuk request yang berjalan bersamaan atau response yang tidak sampai.

Rotasi tidak memperpanjang absolute expiration. Logout dan pencabutan akses mematikan session di database. Request selain GET wajib membawa header `X-CSRF-Token` (didapat dari response login atau `GET /api/auth/me`), tanpa itu ditolak 403.

## Authorization

1. Platform Admin mengelola user, group, koneksi, dan akses. Semua endpoint `/api/admin/...` dicek di backend. Platform Admin bukan level akses database.
2. Akses database diberikan ke group, bukan ke user. User bisa menjadi anggota beberapa group.
3. Level **View**: melihat schema, data, dan riwayat migration. Level **Edit**: View ditambah membuat dan menjalankan migration.
4. **Akses efektif** adalah level tertinggi dari semua group user pada database tersebut. User nonaktif tidak punya akses.
5. Saat privilege naik (dijadikan admin, ditambahkan ke group, akses dinaikkan), session user dirotasi. Saat privilege turun (dinonaktifkan, dikeluarkan dari group, akses diturunkan atau dicabut, password diganti), session dicabut. Perubahan admin dan perubahan session berjalan dalam satu transaksi.

## Migration

1. User Edit membuat draft berisi judul dan SQL. Hanya draft yang bisa diubah dan dijalankan.
2. Eksekusi berjalan dalam satu transaction di database target: commit kalau semua berhasil, rollback kalau ada yang gagal. Hasilnya tercatat sebagai `success` atau `failed` beserta pesan error.
3. Versi hanya diberikan saat berhasil, berurutan per database tanpa lubang, dan aman untuk eksekusi bersamaan.
4. Riwayat linear per database mencatat versi, SQL, pelaksana, waktu, dan status.

Semua error dikembalikan dalam format `{"error": "pesan"}`, dengan status 400 (input tidak valid), 401 (belum login), 403 (tidak punya akses atau CSRF salah), 404, 409 (data konflik), 500, dan 502 (koneksi ke database target gagal).

## Menjalankan test

Pastikan `test-db` berjalan, lalu:

```
export TEST_DATABASE_URL='postgres://test:test@localhost:5434/test?sslmode=disable'
go test ./... -count=1
```

Test integrasi dbmanager, viewer, dan migration butuh schema internal yang sudah diterapkan dan environment variable sendiri:

```
docker compose exec -T test-db psql -U test -d test -c 'DROP SCHEMA IF EXISTS c_test CASCADE; CREATE SCHEMA c_test'
(echo 'SET search_path=c_test;'; cat migrations/001_init.sql) | docker compose exec -T test-db psql -v ON_ERROR_STOP=1 -U test -d test

export PLATFORM_TEST_URL='postgres://test:test@localhost:5434/test?sslmode=disable&search_path=c_test'
export TARGET_TEST_URL='postgres://test:test@localhost:5434/test?sslmode=disable'
POSTGRES_MANAGER_TEST_DATABASE_URL=$PLATFORM_TEST_URL POSTGRES_MANAGER_TEST_TARGET_URL=$TARGET_TEST_URL \
VIEWER_TEST_DATABASE_URL=$PLATFORM_TEST_URL VIEWER_TEST_TARGET_URL=$TARGET_TEST_URL \
MIGRATION_TEST_DATABASE_URL=$PLATFORM_TEST_URL MIGRATION_TEST_TARGET_URL=$TARGET_TEST_URL \
go test ./internal/dbmanager ./internal/migration -count=1 -v

docker compose exec -T test-db psql -U test -d test -c 'DROP SCHEMA c_test CASCADE'
```

Frontend: `cd frontend && npm run build && npm run lint`.

## Struktur repo

```
cmd/server/              entry point
cmd/seed-admin/          command pembuat akun admin
internal/auth/           login, session, CSRF
internal/authz/          hak akses dan akses efektif
internal/admin/          endpoint admin
internal/dbmanager/      koneksi database target dan viewer
internal/migration/      migration
migrations/001_init.sql  schema database internal
frontend/src/            aplikasi React
```

## Anggota

1. Fahd Muhammad Zahid - 13524078
2. Nathanael Gunawan - 13524066
3. Mahatma Brahmana - 13524015