# K03-05-WBD: Database Management Platform

Tugas Besar IF3110 Pengembangan Aplikasi Web 2026, Milestone 1.

Platform web untuk mengatur akses ke database PostgreSQL bersama. Platform Admin mengelola user, group, koneksi database, dan hak akses. User biasa dapat menjelajahi schema dan data database yang boleh diaksesnya, lalu menyusun dan menjalankan migration yang dicatat secara linear dan dijalankan di dalam transaction.

Backend ditulis dalam Go (API JSON di bawah `/api`), frontend memakai React + Vite + Tailwind di folder `frontend/`, dan database dijalankan lewat Docker Compose.

## Status

Semua fitur wajib Milestone 1 sudah tersedia dan tersambung di `cmd/server/main.go`:

1. Login, logout, session dengan idle timeout, absolute expiration, rotasi, grace period, dan perlindungan CSRF
2. Authorization berbasis group dengan level View dan Edit
3. Halaman dan API Platform Admin untuk user, group, anggota group, akses database, dan koneksi database (termasuk test connection)
4. Database viewer: daftar database, schema, tabel, kolom, constraint, index, dan data dengan pagination
5. Migration: draft, editor, eksekusi di dalam transaction, riwayat linear, dan pencatatan versi, SQL, pelaksana, waktu, status, serta pesan error
6. Unit test dan integration test untuk setiap bagian backend

## Prasyarat

1. Go sesuai versi di `go.mod` (saat ini 1.27.2)
2. Node.js LTS dan npm
3. Docker (Docker Desktop di macOS atau Windows)
4. Git

## Menjalankan secara lokal

Perintah di bawah dijalankan dari root repository.

1. Salin file environment, lalu isi `DB_ENCRYPTION_KEY`:
   ```
   cp .env.example .env
   openssl rand -base64 32
   ```
   Tempel hasil `openssl rand -base64 32` sebagai nilai `DB_ENCRYPTION_KEY` di `.env`. Server menolak berjalan kalau key tidak ada atau bukan Base64 dari 32 byte. Jangan mengganti key setelah ada koneksi tersimpan, karena password yang sudah dienkripsi tidak bisa dibuka lagi dengan key baru.

2. Jalankan database:
   ```
   docker compose up -d
   ```

3. Setelah PostgreSQL siap, terapkan schema internal ke `platform-db` (cukup sekali untuk volume baru):
   ```
   docker compose exec -T platform-db psql -v ON_ERROR_STOP=1 -U platform -d platform < migrations/001_init.sql
   ```
   Di PowerShell:
   ```powershell
   Get-Content -Raw migrations/001_init.sql | docker compose exec -T platform-db psql -v ON_ERROR_STOP=1 -U platform -d platform
   ```

4. Jalankan backend:
   ```
   go run ./cmd/server
   ```
   Server berjalan di http://localhost:8080. Cek http://localhost:8080/api/health, harus menampilkan `ok`. Saat start, server membuat akun Platform Admin dari `ADMIN_USERNAME` dan `ADMIN_PASSWORD` kalau username tersebut belum ada.

5. Jalankan frontend di terminal lain:
   ```
   cd frontend
   npm install
   npm run dev
   ```

6. Buka http://localhost:5173 dan login dengan akun admin dari `.env` (default `admin` / `admin123`). Vite meneruskan `/api` ke server Go di port 8080, sehingga cookie session berjalan tanpa CORS.

7. Di halaman Admin, tambahkan koneksi ke database contoh `target-db`:
   1. Host: `localhost`
   2. Port: `5433`
   3. Username: `target`
   4. Password: `target`
   5. Database: `shop`
   6. SSL mode: `disable`

   Tombol test connection benar-benar mencoba konek ke database tersebut. Setelah itu buat group, masukkan user ke group, lalu beri group akses View atau Edit ke koneksi tadi.

Untuk mereset seluruh data platform dan target pada volume Compose:

```
docker compose down -v
docker compose up -d
```

lalu ulangi langkah 3 dan jalankan ulang server.

## Environment variable

Dibaca oleh server (dan `cmd/seed-admin`) dari `.env` atau environment terminal. Environment variable yang sudah terpasang di terminal tidak ditimpa oleh `.env`.

1. `DATABASE_URL`: alamat database internal (`platform-db`).
2. `ADMIN_USERNAME`, `ADMIN_PASSWORD`: akun Platform Admin awal. Hanya dibuat kalau username tersebut belum ada; akun yang sudah ada tidak diubah.
3. `APP_ENV`: `development` atau `production`. Di `production`, cookie session diberi flag Secure.
4. `DB_ENCRYPTION_KEY`: key AES-256 dalam Base64 standar (tepat 32 byte) untuk mengenkripsi dan mendekripsi password koneksi database target.

Khusus test (diatur di terminal, tidak dibaca server): `TEST_DATABASE_URL`, `POSTGRES_MANAGER_TEST_DATABASE_URL`, `POSTGRES_MANAGER_TEST_TARGET_URL`, `VIEWER_TEST_DATABASE_URL`, `VIEWER_TEST_TARGET_URL`, `MIGRATION_TEST_DATABASE_URL`, dan `MIGRATION_TEST_TARGET_URL`. Lihat bagian Menjalankan test.

## Database lokal

1. `platform-db` di port 5435: database internal aplikasi (users, sessions, groups, koneksi, riwayat migration)
2. `target-db` di port 5433: contoh database target untuk demo (`shop`)
3. `test-db` di port 5434: khusus integration test, data hilang saat container restart

## Autentikasi dan session

Login memakai username dan password. Password disimpan sebagai hash bcrypt. Setelah login berhasil, server membuat session ID acak (32 byte dari `crypto/rand`) dan mengirimkannya lewat cookie `session_id` dengan flag HttpOnly, SameSite=Lax, dan Secure (saat `APP_ENV=production`). Database hanya menyimpan hash SHA-256 dari session ID, bukan nilai aslinya. Session ID tidak pernah muncul di URL, body response, maupun log.

Nilai yang dipakai (diatur di `internal/auth/config.go`):

1. **Idle timeout: 30 menit.** Session berakhir kalau tidak ada request selama 30 menit. Cukup longgar untuk membaca schema atau menyusun migration tanpa terputus, tetapi tetap membatasi risiko session yang ditinggal terbuka.
2. **Absolute expiration: 8 jam.** Session berakhir 8 jam setelah login walaupun user terus aktif, kira-kira satu hari kerja. User wajib login ulang setiap hari.
3. **Interval rotasi: 15 menit.** Session ID diganti pada request pertama setelah 15 menit sejak rotasi terakhir, sehingga ID yang bocor hanya berguna dalam waktu singkat. Rotasi juga terjadi saat login (selalu session baru, dan session lama yang dibawa browser dicabut).
4. **Grace period: 30 detik.** Setelah rotasi, ID lama masih diterima selama 30 detik untuk menangani request yang berjalan bersamaan atau response yang tidak sampai ke browser. Setelah itu ID lama ditolak.

Rotasi tidak pernah mengubah waktu absolute expiration. Nama cookie tetap `session_id`; hanya nilainya yang berganti.

Perubahan privilege juga memengaruhi session:

1. **Privilege naik** (user dijadikan admin, ditambahkan ke group, atau akses group ditambah atau dinaikkan): semua session aktif user tersebut ditandai untuk dirotasi, sehingga request berikutnya langsung menerima session ID baru.
2. **Privilege turun** (admin diturunkan, akun dinonaktifkan, user dikeluarkan dari group, akses group diturunkan atau dicabut, group atau koneksi dihapus): semua session user terkait dicabut dan user harus login ulang. Hal yang sama berlaku saat password user diganti oleh admin.

Logout dan pencabutan akses menandai session sebagai dicabut di database (`revoked_at`), jadi session ID yang sama tidak bisa dipakai lagi walaupun masih tersimpan di browser. Session yang melewati absolute expiration mengharuskan login ulang.

Setiap request selain GET, HEAD, dan OPTIONS wajib membawa header `X-CSRF-Token` yang nilainya didapat dari response `POST /api/auth/login` atau `GET /api/auth/me`. Request tanpa token atau dengan token salah ditolak dengan status 403.

## Authorization

Logika hak akses dipisahkan dari autentikasi dan ditempatkan di `internal/authz`.

1. **Platform Admin** mengelola user, group, koneksi database, dan pemberian akses. Semua endpoint `/api/admin/...` memeriksa `authz.RequireAdmin` di backend. Platform Admin bukan tingkatan akses database: admin tetap memerlukan akses lewat group untuk memakai viewer dan migration.
2. **Akses lewat group.** User biasa dapat menjadi anggota satu atau lebih group. Akses database diberikan ke group, bukan langsung ke user. Satu group dapat memiliki level berbeda di setiap database.
3. **Level akses:**
   1. **View**: melihat schema, tabel, kolom, constraint, index, data, serta riwayat dan detail migration.
   2. **Edit**: semua akses View, ditambah membuat, mengubah draft, dan menjalankan migration.
4. **Akses efektif** dihitung oleh `authz.NewPostgresAuthorizer` per pasangan user dan database, yaitu level tertinggi dari seluruh group user pada database tersebut:
   1. Tidak ada group dengan akses: None
   2. Hanya View: View
   3. Ada minimal satu Edit: Edit

   User nonaktif selalu mendapat None. Akses ke satu database tidak berpengaruh ke database lain.
5. **Pemeriksaan di backend.** Setiap handler viewer meminta minimal View, sedangkan create, PATCH, dan execute migration meminta Edit. Tombol di frontend hanya disembunyikan sebagai kenyamanan; penolakan sebenarnya terjadi di backend dengan status 403.

Kapan session dirotasi atau dicabut oleh perubahan admin:

1. Password diganti, akun dinonaktifkan, admin diturunkan, atau user dihapus: revoke session user.
2. User dipromosikan menjadi admin: rotate session user.
3. Anggota ditambahkan ke group: rotate. Anggota dikeluarkan: revoke.
4. Akses group ditambah atau dinaikkan: rotate seluruh anggota. Akses diturunkan atau dicabut: revoke seluruh anggota.
5. Group dihapus: revoke seluruh anggota.
6. Koneksi database dihapus: revoke user di group yang memiliki akses ke koneksi itu.
7. Hanya username yang berubah, atau nilai anggota, akses, dan hak tidak berubah: session tetap.

Jika satu perubahan sekaligus menaikkan dan menurunkan hak, revoke diutamakan. Kebijakan ini konservatif: perubahan group tetap memicu tindakan walaupun group lain mempertahankan akses efektif user. Mengaktifkan kembali akun tidak menghidupkan session yang sudah dicabut. Admin tidak dapat menghapus, menonaktifkan, atau menurunkan hak admin dirinya sendiri.

Perubahan data admin dan perubahan session berjalan di dalam satu transaksi database (lewat `auth.Service.InTx`), sehingga kegagalan salah satunya membatalkan keduanya.

## Database viewer

1. `GET /api/databases`: database yang boleh diakses user beserta levelnya (`view` atau `edit`).
2. `GET /api/databases/{id}/schemas`: daftar schema (tanpa schema sistem).
3. `GET /api/databases/{id}/schemas/{schema}/tables`: daftar tabel.
4. `GET /api/databases/{id}/schemas/{schema}/tables/{table}`: kolom, constraint, dan index.
5. `GET /api/databases/{id}/schemas/{schema}/tables/{table}/rows?page=1&pageSize=50`: data dengan pagination. `pageSize` default 50, maksimal 100.

Nama schema dan tabel dari URL dicek keberadaannya lalu di-quote sebagai identifier sebelum dipakai di query. Kegagalan koneksi ke database target menghasilkan 502 dengan pesan umum, tanpa detail error internal.

Pool koneksi ke database target dibuat dari tabel `db_connections` (password didekripsi saat pool dibuka) dan disimpan per koneksi. Setelah koneksi diubah atau dihapus oleh admin, pool lama ditutup dan koneksi berikutnya dibuat dari data terbaru.

## Migration

1. **Draft.** User dengan akses Edit membuat migration berisi judul dan SQL (`POST /api/databases/{id}/migrations`). Selama masih `draft`, judul dan SQL dapat diubah (`PATCH /api/migrations/{id}`).
2. **Eksekusi.** `POST /api/migrations/{id}/execute` menjalankan SQL di dalam satu transaction pada database target. Kalau seluruh SQL berhasil, transaction di-commit dan status menjadi `success`. Kalau ada statement yang gagal, seluruh perubahan di-rollback, status menjadi `failed`, dan pesan error disimpan di `errorMessage`. Kedua hasil dikembalikan dengan status HTTP 200 beserta data migration terbaru. Kalau koneksi ke database target gagal, migration juga dicatat sebagai `failed` dan response-nya 502.
3. **Versi.** `version` kosong selama draft atau gagal, dan baru diberikan saat migration berhasil. Versi berurutan per database (1, 2, 3, ...) tanpa lubang. Eksekusi pada database yang sama dikunci satu per satu, sehingga dua eksekusi bersamaan tetap mendapat versi berbeda dan berurutan.
4. **Tidak bisa diubah setelah dieksekusi.** Hanya draft yang bisa diubah atau dieksekusi. PATCH atau execute pada migration `success` maupun `failed` ditolak dengan 400. Untuk mencoba ulang migration yang gagal, buat draft baru.
5. **Riwayat linear.** `GET /api/databases/{id}/migrations` mengembalikan riwayat per database, diurutkan berdasarkan versi lalu waktu dibuat. Setiap entri mencatat versi, SQL, pembuat, pelaksana, waktu dibuat, waktu eksekusi, status, dan pesan error. User View dapat melihat riwayat dan detail (`GET /api/migrations/{id}`) tetapi tidak dapat membuat, mengubah, atau menjalankan migration.

Catatan: database target dan database internal adalah dua database yang berbeda. SQL migration di-commit lebih dahulu pada database target, lalu status dan riwayatnya dicatat di database internal. Keduanya tidak berada dalam satu transaksi atomik lintas database. Jika target berhasil commit tetapi pencatatan riwayat gagal, server mengembalikan error 500 secara eksplisit agar kondisi ini dapat ditindaklanjuti.

## Status code

Semua error dikembalikan dalam format `{"error": "pesan"}`.

1. `200` berhasil (termasuk eksekusi migration yang gagal dan sudah di-rollback; lihat field `status`)
2. `201` data baru berhasil dibuat
3. `204` berhasil tanpa body (logout, hapus data)
4. `400` input tidak valid, atau mengubah dan menjalankan migration yang bukan draft
5. `401` belum login atau session habis
6. `403` sudah login tetapi tidak punya akses, atau token CSRF tidak valid
7. `404` data tidak ditemukan
8. `409` konflik data admin, misalnya username atau nama yang sudah dipakai, user yang masih tercatat di riwayat migration, atau admin yang mencoba menonaktifkan atau menghapus dirinya sendiri
9. `500` kesalahan lain di server
10. `502` koneksi ke database target gagal
11. `503` `GET /api/health` saat database internal tidak bisa dihubungi (plain text), atau operasi admin yang membutuhkan session manager saat factory belum dipasang

## Menjalankan test

Pastikan `test-db` berjalan (`docker compose up -d`). Gunakan database khusus testing, bukan `platform-db` atau `target-db`.

Test auth, authz, admin, dan seed admin membuat schema sendiri di `test-db`:

```
export TEST_DATABASE_URL='postgres://test:test@localhost:5434/test?sslmode=disable'
go test ./... -count=1
```

Test integrasi dbmanager, viewer, dan migration memakai environment variable sendiri dan membutuhkan schema internal yang sudah diterapkan. Siapkan schema terpisah di `test-db`, jalankan test, lalu hapus schema tersebut:

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

Target test harus memakai schema `public`, karena test migration memeriksa tabel hasil migration di `public`.

Tanpa environment variable tersebut, integration test yang bersangkutan dilewati (`SKIP`). Hasil PASS saja tidak membuktikan integration test dijalankan; pakai `-v` untuk melihat test yang di-skip. Test integrasi auth tetap jalan tanpa `TEST_DATABASE_URL` karena memakai alamat default `test-db`.

Cakupan test meliputi login, session kedaluwarsa, rotasi dan grace period, logout, CSRF, akses efektif, CRUD admin, tindakan session dan rollback transaksi admin, seed admin, dekripsi password, pool dan invalidasi, viewer, migration yang berhasil dan ter-commit, migration yang gagal dan ter-rollback, penolakan akses, serta kegagalan koneksi database target.

Verifikasi frontend:

```
cd frontend
npm run build
npm run lint
```

## Catatan keamanan

1. Password akun user disimpan sebagai hash bcrypt.
2. Password koneksi database target disimpan terenkripsi dengan AES-256-GCM di `db_connections.password_encrypted`, dengan format `nonce (12 byte) || ciphertext || tag (16 byte)`, nonce acak baru untuk setiap enkripsi, dan tanpa AAD. Key diambil dari `DB_ENCRYPTION_KEY`. Jangan commit `.env`.
3. User dengan akses Edit dapat menjalankan SQL apa pun dengan hak user koneksi target. Sebaiknya koneksi target memakai role PostgreSQL dengan hak minimum yang dibutuhkan, bukan superuser.

## Seed admin manual

Selain otomatis saat server start, akun admin dapat dibuat dengan command terpisah setelah schema internal tersedia:

```
go run ./cmd/seed-admin
```

Command membaca `DATABASE_URL`, `ADMIN_USERNAME`, dan `ADMIN_PASSWORD`. Username setelah trim harus 1 sampai 100 karakter. Password minimal 8 karakter dan maksimal 72 byte. Username yang sudah ada menghasilkan error dan akun lama tidak diubah.

## Struktur repo

```
cmd/server/                    entry point, menyambungkan semua bagian
cmd/seed-admin/                command pembuat akun admin
internal/auth/                 login, session, middleware RequireLogin, CSRF
internal/authz/                pengecekan hak akses dan akses efektif
internal/admin/                endpoint admin user, group, akses, koneksi database
internal/dbmanager/            pool koneksi ke database target dan database viewer
internal/migration/            endpoint dan eksekusi migration
internal/store/                koneksi ke database internal
internal/web/                  helper JSON bersama
migrations/001_init.sql        schema database internal
frontend/src/lib/              API client, auth context, notifikasi
frontend/src/components/       layout, guard, komponen UI bersama
frontend/src/features/auth/    halaman login
frontend/src/features/admin/   halaman admin
frontend/src/features/databases/  daftar database, viewer, editor dan riwayat migration
frontend/src/router.tsx        daftar route
```

## Anggota

1. Fahd Muhammad Zahid - 13524078
2. Nathanael Gunawan - 13524066
3. Mahatma Brahmana - 13524015
