# K03-05-WBD: Database Management Platform

Tugas Besar IF3110 Pengembangan Aplikasi Web 2026, Milestone 1.

Platform web untuk mengatur akses ke database PostgreSQL bersama: login, pengelolaan user dan group, database viewer, serta migration yang tercatat dan dijalankan di dalam transaction.

## State

1. Struktur project Go
2. Docker Compose berisi 3 PostgreSQL (internal, target contoh, testing)
3. Koneksi server ke database internal
4. Schema database internal (`migrations/001_init.sql`)
5. Interface antar modul beserta stub (`auth`, `authz`, `dbmanager`)


## Prasyarat

1. Go 1.22 atau lebih baru
2. Docker Desktop
3. Git

## Menjalankan secara lokal

1. Copy file environment:
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
4. Jalankan server:
   ```
   go run ./cmd/server
   ```
5. Buka:
   1. http://localhost:8080/health, harus menampilkan `ok`
   2. http://localhost:8080/me, harus menampilkan `hello dev, admin=true` (masih user palsu dari stub)

Untuk mereset database dari nol:

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
internal/auth/       login, session, middleware
internal/authz/      pengecekan hak akses
internal/admin/      halaman admin
internal/dbmanager/  koneksi ke database target
internal/migration/  fitur migration
internal/store/      koneksi ke database internal
internal/web/        helper web bersama
migrations/          schema database internal
web/templates/       template HTML
web/static/          CSS dan JS
```

## Anggota

1. Fahd Muhammad Zahid - 13524078
2. Nathanael Gunawan - 13524066
3. Mahatma Brahmana - 13524015
