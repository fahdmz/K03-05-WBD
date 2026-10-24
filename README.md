# K03-05-WBD: Database Management Platform

Tugas Besar IF3110 Pengembangan Aplikasi Web 2026, Milestone 1.

Platform web untuk mengatur akses ke database PostgreSQL bersama: login, pengelolaan user dan group, database viewer, serta migration yang tercatat dan dijalankan di dalam transaction.

## Status implementasi

1. Struktur project Go
2. Docker Compose berisi 3 PostgreSQL (internal, target contoh, testing)
3. Koneksi server ke database internal
4. Schema database internal (`migrations/001_init.sql`)
5. Authorizer PostgreSQL, endpoint admin, halaman React admin, dan command seed admin (bagian B)
6. Pengelolaan session pada mutasi admin melalui factory yang terikat transaksi

Branch ini belum terintegrasi penuh dengan A dan C: server masih memakai autentikasi stub dan `dbmanager.Stub`, serta belum memasang factory session asli. Operasi yang membutuhkan session manager mengembalikan 503 jika factory belum dipasang. Keberhasilan test connection dengan stub belum membuktikan koneksi ke database target.


## Prasyarat

1. Go sesuai versi di `go.mod` (saat ini 1.27.2)
2. Docker Desktop
3. Git
4. Node.js dan npm yang kompatibel dengan dependency frontend di `frontend/package.json`

## Menjalankan secara lokal

Perintah berikut memakai PowerShell dari root repository. Jangan menimpa `.env` yang sudah berisi konfigurasi lokal.

1. Untuk setup pertama, salin file environment:
   ```powershell
   Copy-Item .env.example .env
   ```
   `.env.example` saat ini hanya berisi `DATABASE_URL`. Tambahkan `DB_ENCRYPTION_KEY` sesuai bagian enkripsi di bawah sebelum menjalankan server. Server dan command seed membaca `.env`; environment variable terminal yang sudah terpasang tetap berlaku.
2. Jalankan database:
   ```
   docker compose up -d
   ```
3. Setelah PostgreSQL siap, buat tabel internal pada database baru:
   ```powershell
   Get-Content -Raw migrations/001_init.sql | docker compose exec -T platform-db psql -v ON_ERROR_STOP=1 -U platform -d platform
   ```
4. Jalankan server:
   ```
   go run ./cmd/server
   ```
5. Buka:
   1. http://localhost:8080/api/health, harus menampilkan `ok`
   2. http://localhost:8080/api/me, endpoint development dengan user palsu dari stub; bukan `/api/auth/me` milik implementasi A

6. Jalankan frontend di terminal kedua:
   ```powershell
   cd frontend
   npm ci
   npm run dev
   ```
   Buka http://localhost:5173. Vite meneruskan `/api` ke server Go di port 8080. Halaman admin saat ini berdiri sendiri, belum menggunakan router dan login asli A.

Untuk mereset database dari nol (menghapus seluruh data platform dan target pada volume Compose):

```
docker compose down -v
docker compose up -d
```

lalu ulangi langkah 3.

## Database lokal

1. `platform-db` di port 5435: database internal aplikasi
2. `target-db` di port 5433: contoh database target untuk demo
3. `test-db` di port 5434: khusus integration test, data hilang saat restart

## Struktur repo

```
cmd/server/          entry point
cmd/seed-admin/      command pembuat akun admin
internal/auth/       login, session, middleware
internal/authz/      pengecekan hak akses
internal/admin/      handler API admin, helper transaksi/session, dan test
internal/dbmanager/  koneksi ke database target
internal/migration/  fitur migration
internal/store/      koneksi ke database internal
internal/web/        helper web bersama
migrations/          schema database internal
frontend/src/features/admin/  halaman, API client, tipe, dan CSS admin
```

## Aturan akses efektif (B)

`authz.NewPostgresAuthorizer` membaca `group_members` dan `group_db_access`. Akses dihitung untuk setiap pasangan user dan database dengan mengambil level tertinggi dari seluruh grup user:

| Akses grup pada database yang sama | Akses efektif |
| --- | --- |
| Tidak ada | None (0) |
| View saja | View (1) |
| View dan Edit, atau Edit saja | Edit (2) |

User yang tidak ditemukan atau nonaktif mendapat None. Akses ke database lain tidak ikut terbawa. Platform admin adalah hak mengelola platform, bukan akses otomatis ke data database; admin tetap memerlukan akses melalui grup untuk viewer dan migration.

`Require` menerima minimum View atau Edit dan menolak level yang tidak cukup. Error query tidak menghasilkan izin. Handler admin memeriksa user login dan `authz.RequireAdmin`; pemeriksaan akses database pada viewer/migration dilakukan oleh bagian C menggunakan authorizer ini.

## Seed admin pertama (B)

Setelah schema internal tersedia, atur `DATABASE_URL`, `ADMIN_USERNAME`, dan `ADMIN_PASSWORD`, lalu jalankan command berikut dari root repository. Password dibaca interaktif agar tidak ditulis sebagai literal dalam riwayat command:

```powershell
$env:ADMIN_USERNAME = 'admin'
$seedPassword = Read-Host 'Password admin' -AsSecureString
$env:ADMIN_PASSWORD = [System.Net.NetworkCredential]::new('', $seedPassword).Password
try {
    go run ./cmd/seed-admin
} finally {
    Remove-Item Env:ADMIN_PASSWORD -ErrorAction SilentlyContinue
    $seedPassword.Dispose()
}
```

Username setelah trim harus 1 sampai 100 karakter tanpa NUL. Password minimal 8 karakter dan maksimal 72 byte. Command membuat user aktif dengan `is_admin=true` dan password bcrypt. Username duplikat menghasilkan error; akun lama tidak diubah. Command ini tidak membuat session login. Jangan commit password admin atau `.env`.

## Enkripsi koneksi database (B dan C)

Password akun user disimpan sebagai hash bcrypt. Password koneksi database target memakai AES-256-GCM agar dapat didekripsi oleh manager C.

| Kontrak | Nilai |
| --- | --- |
| Environment variable | `DB_ENCRYPTION_KEY` |
| Encoding key | Standard Base64 dari tepat 32 byte acak |
| Kolom penyimpanan | `db_connections.password_encrypted`, tipe BYTEA |
| Format byte | `nonce || ciphertext || tag` |
| Nonce | 12 byte acak baru untuk setiap enkripsi |
| Authentication tag | 16 byte, sudah termasuk output GCM Seal |
| AAD | Tidak digunakan (`nil`) |

Buat key sekali untuk environment lokal:

```powershell
$keyBytes = New-Object byte[] 32
$keyGenerator = [System.Security.Cryptography.RandomNumberGenerator]::Create()
try {
    $keyGenerator.GetBytes($keyBytes)
} finally {
    $keyGenerator.Dispose()
}
$env:DB_ENCRYPTION_KEY = [Convert]::ToBase64String($keyBytes)
```

Simpan nilai yang sama secara privat sebagai `DB_ENCRYPTION_KEY` di `.env` atau konfigurasi environment. Nilai yang hanya diatur lewat `$env:` berlaku pada terminal tersebut dan proses anaknya. Jangan menghasilkan key baru setiap restart: password yang sudah tersimpan membutuhkan key lama untuk didekripsi. B dan C harus menggunakan key yang sama; isi BYTEA bukan string Base64.

`POST /api/admin/databases/test` meneruskan password plaintext dalam `dbmanager.ConnConfig` ke `Manager.Test` sebelum penyimpanan, dengan timeout 5 detik. Endpoint ini tidak menyimpan konfigurasi. Kegagalan manager menghasilkan 502 dengan pesan umum. Update dan delete koneksi memanggil `Manager.Invalidate(dbID)` setelah transaksi berhasil commit.

## Endpoint dan session admin (B)

| Method | Endpoint |
| --- | --- |
| GET, POST | `/api/admin/users` |
| PATCH, DELETE | `/api/admin/users/{id}` |
| GET, POST | `/api/admin/groups` |
| PATCH, DELETE | `/api/admin/groups/{id}` |
| PUT | `/api/admin/groups/{id}/members` |
| PUT | `/api/admin/groups/{id}/access` |
| GET, POST | `/api/admin/databases` |
| PATCH, DELETE | `/api/admin/databases/{id}` |
| POST | `/api/admin/databases/test` |

PUT anggota dan akses mengganti seluruh daftar, bukan menambah sebagian. Array kosong menghapus seluruh anggota atau akses grup. GET grup menyertakan `memberIds` dan `access` untuk form pengaturan.

| Perubahan | Tindakan session |
| --- | --- |
| Password diganti, akun dinonaktifkan, admin diturunkan, user dihapus | Revoke session user |
| User dipromosikan menjadi admin | Rotate session user |
| Anggota ditambahkan / dikeluarkan dari grup | Rotate / revoke user terkait |
| Akses grup ditambah atau dinaikkan / diturunkan atau dicabut | Rotate / revoke seluruh anggota grup |
| Grup dihapus | Revoke seluruh anggota grup |
| Koneksi database dihapus | Revoke user di grup yang memiliki akses ke koneksi itu |
| Username saja atau nilai anggota/akses/hak yang tidak berubah | Session tetap |

Jika satu perubahan sekaligus menaikkan dan menurunkan hak, revoke diutamakan. Kebijakan session bersifat konservatif: perubahan grup tetap memicu tindakan meskipun grup lain mempertahankan akses efektif user. Mengaktifkan kembali akun tidak menghidupkan session yang sudah dicabut. Admin tidak dapat menghapus, menonaktifkan, atau menurunkan hak admin sendiri.

Mutasi data dan pemanggilan session manager berada dalam satu transaksi. Kegagalan salah satunya membatalkan keduanya, asalkan manager menggunakan transaksi yang diberikan factory. Factory yang belum dipasang menghasilkan 503 untuk operasi yang membutuhkan manager. Grup tanpa anggota dapat diubah/dihapus tanpa tindakan session.

## Pengujian bagian B

Jalankan dari root repository dengan database testing terpisah:

```powershell
docker compose up -d test-db
docker compose exec test-db pg_isready -U test -d test
$env:TEST_DATABASE_URL = 'postgres://test:test@localhost:5434/test?sslmode=disable'
go test -v ./internal/authz ./internal/admin ./cmd/seed-admin -count=1
```

Tanpa `TEST_DATABASE_URL`, integration test dilewati; hasil PASS saja tidak membuktikan integration test dijalankan. Gunakan database khusus testing, bukan database platform atau target yang berisi data penting. Test mencakup akses efektif, CRUD, input gagal, penolakan akses, enkripsi, seed admin, tindakan session, rollback, dan invalidasi pool.

Test session admin memakai manager khusus test dalam transaksi PostgreSQL. Test tersebut membuktikan pemanggilan revoke/rotate dan rollback bagian B, bukan rotasi cookie, token, grace period, atau absolute expiration pada implementasi A.

Untuk verifikasi frontend:

```powershell
cd frontend
npm ci
npm run build
npm run lint
```

## Catatan integrasi untuk PR (A, B, C)

1. **Session A:** pasang factory melalui `WithSessionManagerFactory` pada hasil `admin.NewHandler` dan `admin.NewResourceHandler`. Method mengembalikan salinan handler, sehingga gunakan nilai kembalian sebelum mendaftarkan route. Kontrak tambahan B adalah `type SessionManagerFactory func(tx pgx.Tx) SessionManager`; interface manager memiliki `RevokeUserSessions(context.Context, int64) error` dan `RotateUserSessions(context.Context, int64) error`. Adapter A harus menjalankan perubahan session lewat `tx` tersebut, bukan pool atau transaksi terpisah, dan tidak melakukan commit sendiri. Adapter ini belum tersedia di wiring lokal dan perlu disepakati dengan A.
2. **Autentikasi A:** ganti middleware login stub dengan session asli serta CSRF. Implementasi rotasi A harus mengganti identifier dengan grace period tanpa memperpanjang absolute expiration. Uji dengan session dan cookie asli setelah integrasi.
3. **Manager C:** ganti `dbmanager.Stub` dengan implementasi asli yang mengikuti format enkripsi di atas. Sambungkan `authz.NewPostgresAuthorizer(db)` ke handler viewer/migration C.
4. **Frontend A/B:** gabungkan halaman admin ke layout/router A dengan guard admin. API client admin memakai `credentials: 'include'`, tetapi CSRF token awal masih kosong. Isi melalui `setAdminCsrfToken` dari response login atau `/api/auth/me`, atau integrasikan dengan client bersama A. Teruskan ID user ke prop `currentUserId` pada `AdminPage`. Tambahkan redirect login saat 401 melalui fondasi A; client admin lokal saat ini hanya menampilkan pesan error.
5. **Uji alur gabungan:** login admin, kelola user/grup/koneksi, login user View/Edit, ubah hak melalui admin, lalu pastikan session dan akses viewer/migration mengikuti perubahan. Test connection harus diuji dengan koneksi nyata yang berhasil dan gagal.

Nilai timeout session, interval rotasi, dan grace period masih menjadi dokumentasi A. Bagian ini tidak menetapkan nilainya atau mengklaim integrasi selesai.

## Anggota

1. Fahd Muhammad Zahid - 13524078
2. Nathanael Gunawan - 13524066
3. Mahatma Brahmana - 13524015
