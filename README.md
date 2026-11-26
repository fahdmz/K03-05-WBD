# K03-05-WBD: Database Management Platform

Tugas Besar IF3110 Pengembangan Aplikasi Web 2026, Milestone 2.

Platform web untuk mengatur akses ke database PostgreSQL bersama. Platform Admin mengelola user, group, leader group, koneksi database, dan hak akses. User menjelajahi schema dan data database yang boleh diaksesnya, lalu menyusun migration yang wajib divalidasi, direview, dan disetujui leader sebelum dijalankan secara asynchronous oleh worker terpisah.

Milestone 2 mengubah eksekusi migration dari pemanggilan langsung di request HTTP menjadi alur bertahap. Migration kini punya revisi dengan SQL up dan SQL down, diajukan sebagai change request, disetujui oleh leader setiap group yang punya akses ke database tersebut, lalu dimasukkan ke antrean. Worker mengambil job dari antrean, memegang lock per database, memvalidasi ulang, menjalankan SQL di database target dalam transaction (atau per batch untuk data besar), dan mencatat setiap attempt ke migration history yang tidak bisa diubah. Selain itu M2 menambahkan rollback dan forward berbasis posisi aktif, pemulihan job ketika worker mati, log JSON terstruktur dengan tracing, dan metrik Prometheus.

Backend: Go (API JSON di `/api`) dengan dua proses, `cmd/server` dan `cmd/worker`. Frontend: React + Vite + Tailwind di `frontend/`. Database: PostgreSQL lewat Docker Compose.

## Alur minimum

Alur berikut adalah jalur utama yang didukung platform dari awal sampai akhir. Detail setiap langkah dijelaskan di bagian-bagian berikutnya.

1. **Login**: user masuk dengan username dan password, server membuat session dan token CSRF.
2. **Access Control**: setiap endpoint memeriksa akses efektif user (View atau Edit) pada database yang diminta.
3. **Select Database**: halaman utama hanya menampilkan database yang boleh diakses user.
4. **View Schema/Data**: tab Database explorer menampilkan schema, struktur tabel, dan data per halaman.
5. **Create Migration**: user Edit membuat migration berisi revisi draft (SQL up, SQL down atau alasan irreversible, mode normal atau batch).
6. **Validation & Review**: revisi divalidasi (struktur dan dry run), lalu diajukan sehingga revisi dibekukan dan terbentuk change request berstatus `pending_review`.
7. **Approval**: leader setiap group yang punya akses ke database memberi keputusan, dan change request menjadi `approved` setelah semua menyetujui.
8. **Queue**: user Edit menekan Jalankan, server membuat baris di tabel `jobs` dan membalas `202 Accepted` dengan `jobId` dan `executionId`.
9. **Worker**: proses worker mengambil job dari antrean dengan `FOR UPDATE SKIP LOCKED`.
10. **Lock**: worker memegang lock database dengan lease dan heartbeat, sehingga hanya satu migration berjalan per database.
11. **Revalidation**: tepat sebelum eksekusi, approval, akses pengaju, posisi migration, dan job yang menahan diperiksa ulang.
12. **Execute**: SQL dijalankan di database target, dalam satu transaction (mode normal) atau satu transaction per batch.
13. **Commit/Rollback**: transaction di-commit kalau berhasil dan di-rollback kalau gagal, dengan bukti eksekusi ditulis di transaction yang sama.
14. **Update Migration History**: setiap attempt yang dijalankan dicatat ke `migration_history`, dan hanya attempt sukses yang menggeser posisi aktif.
15. **Audit Log**: setiap langkah menghasilkan log JSON dengan `trace_id`, sedangkan history, change request, dan approval tersimpan di database platform.

## Prasyarat

1. Go sesuai versi di `go.mod`
2. Node.js LTS
3. Docker
4. Git

## Menjalankan secara lokal

Database lokal yang dipakai adalah `platform-db` (port 5435, database internal platform), `target-db` (port 5433, database contoh `shop` untuk demo dan uji data besar), dan `test-db` (port 5434, khusus automated test, datanya di tmpfs sehingga hilang setiap container berhenti).

Langkah dari root repository:

1. Siapkan environment, lalu isi `DB_ENCRYPTION_KEY` di `.env` dengan hasil `openssl rand -base64 32`:
   ```
   cp .env.example .env
   openssl rand -base64 32
   ```
2. Jalankan semua database:
   ```
   docker compose up -d
   ```
3. Terapkan schema internal secara berurutan, `001` lalu `002` (sekali untuk volume baru):
   ```
   docker compose exec -T platform-db psql -v ON_ERROR_STOP=1 -U platform -d platform < migrations/001_init.sql
   docker compose exec -T platform-db psql -v ON_ERROR_STOP=1 -U platform -d platform < migrations/002_m2.sql
   ```
4. Jalankan server API (http://localhost:8080, port tetap di kode):
   ```
   go run ./cmd/server
   ```
5. Jalankan worker di terminal lain. Worker membaca `.env` yang sama karena butuh `DATABASE_URL` dan `DB_ENCRYPTION_KEY` untuk membuka koneksi ke database target:
   ```
   go run ./cmd/worker
   ```
   Jumlah goroutine pemroses dan alamat endpoint metrik bisa diatur, misalnya untuk menjalankan worker kedua di mesin yang sama:
   ```
   WORKER_CONCURRENCY=2 WORKER_METRICS_ADDR=:9092 go run ./cmd/worker
   ```
6. Jalankan frontend di terminal lain:
   ```
   cd frontend
   npm install
   npm run dev
   ```
7. Buka http://localhost:5173 dan login sebagai admin (default `admin` / `admin123`, dibuat otomatis saat server start).
8. Di halaman Admin, tambahkan koneksi ke database contoh: host `localhost`, port `5433`, user `target`, password `target`, database `shop`, SSL mode `disable`.
9. Masih di halaman Admin, buka tab Group dan buat group. Klik "Anggota" pada group tersebut untuk memasukkan user, lalu klik "Akses" untuk memberi level View atau Edit per database. Terakhir klik "Edit" dan pilih user di "Leader group". Leader hanya bisa dipilih dari anggota group, jadi masukkan anggotanya lebih dulu. Ulangi untuk setiap group yang punya akses, karena setiap group tersebut wajib punya leader agar change request bisa disetujui.
10. Opsional, isi dataset besar untuk uji mode batch (lihat bagian Ketentuan pengujian dataset besar):
    ```
    go run ./cmd/seed-bigdata
    ```

File `002_m2.sql` berjalan dalam satu `BEGIN ... COMMIT` dan memakai `CREATE TABLE` tanpa `IF NOT EXISTS`, sehingga tidak bisa diterapkan dua kali. Kalau isi `002` berubah (misalnya setelah pull), `platform-db` harus dibuat ulang lalu `001` dan `002` diterapkan lagi. Cara yang tidak menyentuh data `target-db` adalah menghapus volume platform saja:

```
docker compose stop platform-db
docker compose rm -f platform-db
docker volume rm k03-05-wbd_platform-data
docker compose up -d platform-db
```

Nama volume mengikuti nama project Compose, cek dengan `docker volume ls` bila berbeda. Untuk mereset semua data (termasuk `target-db`), jalankan `docker compose down -v`, `docker compose up -d`, lalu ulangi langkah 3. Schema `_platform` di database target berisi bukti eksekusi lama dan boleh dibiarkan, karena execution ID baru selalu UUID acak.

### Environment variable

| Variable | Dipakai oleh | Keterangan |
|---|---|---|
| `DATABASE_URL` | server, worker | Alamat database internal (`platform-db`). |
| `ADMIN_USERNAME`, `ADMIN_PASSWORD` | server | Akun Platform Admin awal, hanya dibuat kalau username belum ada. |
| `APP_ENV` | server | `development` atau `production`. Di `production`, cookie session diberi flag Secure. |
| `DB_ENCRYPTION_KEY` | server, worker | Key AES-256 (Base64 dari 32 byte) untuk mengenkripsi password koneksi database target. Jangan diganti setelah ada koneksi tersimpan. |
| `WORKER_CONCURRENCY` | worker | Jumlah loop claim paralel dalam satu proses worker. Default 4. Nilai bukan angka atau kurang dari 1 diabaikan dengan log `worker.bad_concurrency`. |
| `WORKER_METRICS_ADDR` | worker | Alamat endpoint `/metrics` worker. Default `:9091`. |
| `TARGET_DATABASE_URL` | seed-bigdata | Database tujuan seed. Default `postgres://target:target@localhost:5433/shop?sslmode=disable`. |
| `TEST_DATABASE_URL` | test | Default `postgres://test:test@localhost:5434/test?sslmode=disable`. |

`WORKER_CONCURRENCY`, `WORKER_METRICS_ADDR`, dan `TARGET_DATABASE_URL` tidak ada di `.env.example` karena opsional. Ketiganya bisa ditambahkan ke `.env` atau di-set langsung di terminal.

### Seed dataset besar

`cmd/seed-bigdata` membuat tabel `orders_big` (kolom `id BIGSERIAL PRIMARY KEY`, `customer_id`, `price`, `status`, `updated_at`) di database target, mengisinya dengan data deterministik dari `generate_series`, menjalankan `VACUUM ANALYZE`, lalu mengukur durasi `UPDATE orders_big SET price = price * 1.1` tanpa batch. UPDATE pengukuran itu selalu di-`ROLLBACK` agar dataset tetap sama untuk uji batch, lalu tabel di-`VACUUM` untuk membuang dead tuple. Kalau tabel sudah berisi cukup row, proses insert dilewati.

| Flag | Default | Keterangan |
|---|---|---|
| `-rows` | 3000000 | Jumlah row `orders_big`. |
| `-chunk` | 500000 | Jumlah row per satu `INSERT`. |
| `-reset` | false | `DROP TABLE orders_big` lalu buat ulang. |
| `-measure-only` | false | Hanya mengukur UPDATE tanpa batch. Tabel harus sudah ada. |
| `-url` | dari `TARGET_DATABASE_URL` | URL database target, menimpa env. |

`-reset` dan `-measure-only` tidak boleh dipakai bersamaan. Kalau UPDATE tanpa batch selesai dalam 1 detik atau kurang, program mencetak peringatan `seed.too_fast` agar `-rows` dinaikkan lalu seed diulang dengan `-reset`. Statement timeout koneksi seed di-set 0 supaya proses panjang tidak terpotong.

## Autentikasi dan session

Password disimpan sebagai hash bcrypt. Setelah login, server membuat session ID acak 32 byte yang dikirim lewat cookie `session_id` (HttpOnly, SameSite=Lax, Secure di production). Database hanya menyimpan hash SHA-256-nya. Session ID tidak muncul di URL, response, maupun log.

Nilai yang dipakai (`internal/auth/config.go`):

1. **Idle timeout 30 menit**: cukup longgar untuk membaca schema atau menyusun migration, tetapi membatasi risiko session yang ditinggal terbuka.
2. **Absolute expiration 8 jam**: kira-kira satu hari kerja, setelah itu wajib login ulang walaupun masih aktif.
3. **Rotasi setiap 15 menit**: session ID diganti pada request berikutnya, sehingga ID yang bocor cepat tidak berlaku. Rotasi juga terjadi saat login dan saat privilege naik.
4. **Grace period 30 detik**: ID lama masih diterima sebentar setelah rotasi, untuk request yang berjalan bersamaan atau response yang tidak sampai.

Rotasi tidak memperpanjang absolute expiration. Logout dan pencabutan akses mematikan session di database. Request selain GET wajib membawa header `X-CSRF-Token` (didapat dari response login atau `GET /api/auth/me`), tanpa itu ditolak 403.

## Authorization

Platform Admin mengelola user, group, leader group, koneksi, dan akses, dan semua endpoint `/api/admin/...` dicek di backend. Platform Admin bukan level akses database. Akses database diberikan ke group, bukan ke user, dan user bisa menjadi anggota beberapa group. Level **View** memberi hak melihat schema, data, change request, job, dan migration history. Level **Edit** menambahkan hak membuat dan merevisi migration, memvalidasi, mengajukan, menjalankan change request yang sudah disetujui, serta mengajukan rollback, forward, resume, dan revert. **Akses efektif** adalah level tertinggi dari semua group user pada database tersebut, dan user nonaktif tidak punya akses.

Saat privilege naik (dijadikan admin, ditambahkan ke group, akses dinaikkan), session user dirotasi. Saat privilege turun (dinonaktifkan, dikeluarkan dari group, akses diturunkan atau dicabut, password diganti), session dicabut. Perubahan admin dan perubahan session berjalan dalam satu transaksi.

Peran **leader** adalah atribut group (`groups.leader_user_id`), bukan level akses. Leader dipilih dari anggota group dan berhak memberi keputusan approval untuk change request pada database yang bisa diakses group-nya. Hak menjalankan change request tetap mensyaratkan akses Edit.

## Frontend SPA dan responsif

Frontend adalah single page application dengan React Router (`frontend/src/router.tsx`). Semua halaman selain login dibungkus `RequireAuth` dan layout bersama berisi navigasi Databases, Reviews, dan Admin (hanya untuk Platform Admin).

| Path | Halaman | Isi |
|---|---|---|
| `/login` | LoginPage | Form login. |
| `/` | DatabasesPage | Daftar database yang bisa diakses, lalu tab Database explorer (schema, struktur, data per halaman), Migration (daftar migration, editor revisi, validasi, pengajuan, execution plan), History (posisi aktif, tombol rollback/forward, Hide rollback), dan Jobs (daftar job database). |
| `/reviews` | ReviewsPage | Change request `pending_review` yang menunggu keputusan dari group yang dipimpin user. |
| `/change-requests/:id` | ChangeRequestPage | Detail change request, status approval per group, form approve/reject, hasil validasi, SQL up/down, dan tombol Jalankan. |
| `/jobs/:id` | JobPage | Status job, attempt, progres batch, error terakhir beserta SQLSTATE, serta tombol resume/revert untuk job batch berstatus partial. |
| `/admin` | AdminPage | Hanya Platform Admin (`RequireAdmin`). |
| `*` | NotFoundPage | Halaman tidak ditemukan. |

Status job dipantau dengan polling, karena eksekusi berjalan di worker dan API hanya mengembalikan `202`. JobPage memanggil `GET /api/jobs/{id}` setiap 1,5 detik selama status job `queued`, `running`, atau `uncertain`, dan berhenti begitu status final. Kalau request gagal karena jaringan atau status 5xx, polling dicoba lagi setiap 3 detik, sedangkan error 4xx dianggap permanen dan polling dihentikan. Tab Jobs memanggil `GET /api/databases/{id}/jobs` setiap 3 detik selama masih ada job aktif. Halaman change request dan history tidak melakukan polling dan menyediakan tombol Muat ulang.

Tampilan dirancang mobile first dengan breakpoint Tailwind. Navigasi memakai `flex-wrap`, susunan dua kolom (daftar di kiri, konten di kanan) baru aktif di layar `xl` dan bertumpuk di layar kecil, dan kelompok tombol berubah dari vertikal ke horizontal di `sm`. Aturan utamanya, konten lebar tidak boleh melebarkan halaman: tabel data dan struktur dibungkus `overflow-x-auto` sehingga di-scroll horizontal di dalam komponennya sendiri, editor SQL (textarea) memakai `overflow-x-auto` dengan `resize-y`, dan blok SQL hasil (`pre`) diberi `max-h` dengan `overflow-auto`. Teks panjang seperti execution ID memakai `break-all`, dan tab database bisa di-scroll horizontal bila tidak muat.

## Migration workflow dan approval

Sebuah migration adalah wadah untuk satu perubahan schema atau data pada satu database, dan isinya disimpan sebagai revisi di `migration_revisions`. Setiap revisi berisi SQL up, SQL down atau penanda irreversible beserta alasannya, mode (`normal` atau `batch`), serta untuk mode batch nama tabel, kolom cursor, dan ukuran batch (1 sampai 100000). Membuat migration menghasilkan revisi 1 berstatus `draft`. Selama revisi masih draft, `PATCH /api/migrations/{id}` menimpa isinya. Begitu diajukan, revisi berstatus `submitted` dan SQL up/down-nya tidak bisa diubah lagi. Perubahan berikutnya pada migration yang belum pernah dieksekusi membuat revisi draft baru dengan nomor berikutnya, sedangkan migration yang sudah punya versi atau riwayat eksekusi ditolak dengan pesan bahwa migration bersifat immutable dan harus dibuat migration baru.

Revisi yang tidak punya SQL down wajib ditandai **irreversible** dengan alasan yang diisi. Constraint database menjamin keduanya (`irreversible OR sql_down IS NOT NULL` dan alasan wajib ada bila irreversible). Migration irreversible tidak bisa di-rollback, baik saat pengajuan maupun saat revalidasi di worker. Migration dari M1 dikonversi oleh `002_m2.sql` menjadi revisi 1 irreversible dengan alasan "Migration M1 tanpa SQL down", karena M1 tidak menyimpan SQL down.

Validasi berjalan dua tahap dan bisa dipanggil sendiri lewat `POST /api/migrations/{id}/validate` atau otomatis saat `POST /api/migrations/{id}/submit`. Tahap struktur memeriksa SQL up wajib ada, SQL down atau alasan irreversible, mode yang valid, larangan perintah kontrol transaksi (`BEGIN`, `COMMIT`, `END`, `ROLLBACK`, `SAVEPOINT`, `RELEASE`, `START TRANSACTION`, `PREPARE TRANSACTION`), dan khusus batch: placeholder `{{lo}}` dan `{{hi}}` wajib ada di SQL up dan SQL down serta masing-masing hanya boleh satu statement. Pemecahan statement mengabaikan string, identifier ber-quote, komentar, dan dollar quote. Tahap kedua bergantung pada mode. Mode normal menjalankan dry run di database target: SQL up lalu SQL down dalam satu transaction yang selalu di-rollback, dengan `statement_timeout` 5 detik, `lock_timeout` 2 detik, dan batas total 12 detik. Timeout dan tunggu lock (`57014`, `55P03`) dicatat sebagai peringatan, sedangkan error lain menjadi error validasi beserta SQLSTATE-nya. Mode batch tidak menjalankan dry run, melainkan memeriksa bahwa tabel dan kolom batch ada dan kolom tersebut punya unique index valid satu kolom tanpa predikat. Pengajuan hanya berhasil bila validasi lolos. Dalam satu transaction, revisi dibekukan, change request `apply` dibuat, dan hasil validasi disimpan di `change_requests.validation` agar bisa dibaca reviewer.

Approval dihitung dinamis setiap kali dibaca, tidak disimpan sebagai satu flag. Group yang wajib menyetujui adalah semua group yang punya baris di `group_db_access` untuk database tersebut, baik level View maupun Edit. Setiap group harus punya leader aktif, dan keputusan hanya dihitung bila diberikan oleh leader group saat ini. Akibatnya, kalau leader diganti atau dinonaktifkan, keputusan lama tidak lagi dihitung. Kalau ada group baru yang diberi akses, group itu juga harus menyetujui. Change request dianggap approved bila daftar group tidak kosong, semua group punya leader aktif, semuanya memberi `approve`, dan tidak ada yang `reject`. Leader yang memimpin beberapa group sekaligus memberi keputusan untuk semua group tersebut dalam satu aksi. Keputusan boleh diberikan selama status `pending_review` atau `approved`, dan keputusan baru menimpa keputusan sebelumnya dari group yang sama.

Penolakan wajib menyertakan alasan (komentar maksimal 2000 karakter, wajib untuk reject dan opsional untuk approve). Satu reject langsung membuat change request berstatus `rejected`, dan change request yang ditolak tidak bisa dijalankan. Perubahan selanjutnya dilakukan dengan revisi atau change request baru. Saat tombol Jalankan ditekan (`POST /api/change-requests/{id}/run`), server memeriksa akses Edit, memastikan status `approved`, lalu menghitung ulang approval. Kalau approval ternyata tidak lagi lengkap karena perubahan akses atau leader, status dikembalikan ke `pending_review` dan request ditolak dengan 409.

Revalidasi di worker (`workflow.Revalidate`) berjalan tepat sebelum eksekusi dalam satu transaction platform. Yang diperiksa ulang adalah status change request, approval dinamis seperti di atas, pengaju masih aktif dan masih punya akses Edit, tidak ada job lain di database yang sama berstatus `partial` atau `uncertain` (kecuali satu keluarga eksekusi untuk resume/revert), dan aturan posisi: revisi harus `submitted`, `apply` hanya untuk migration yang belum pernah diterapkan, `rollback` hanya untuk versi aktif teratas yang reversible, `forward` hanya untuk versi terkecil di atas posisi aktif, dan `resume`/`revert` hanya untuk job tujuan berstatus `partial`. Kalau revalidasi gagal, job dan change request menjadi `failed` dengan pesan "Validasi ulang gagal: ...". Revalidasi dilewati hanya untuk job yang hasil attempt sebelumnya mungkin sudah ada di target, karena untuk job seperti itu bukti di database target yang menentukan.

Status change request:

| Status | Arti |
|---|---|
| `pending_review` | Menunggu keputusan leader. |
| `approved` | Semua leader menyetujui, siap dijalankan. |
| `rejected` | Ada leader yang menolak. |
| `queued` | Job sudah masuk antrean (termasuk saat menunggu retry atau tertahan). |
| `running` | Sedang dikerjakan worker. |
| `success` | Selesai dan di-commit. |
| `failed` | Gagal permanen, revalidasi gagal, atau retry habis tanpa batch committed. |
| `partial` | Mode batch berhenti setelah sebagian batch committed. Di UI tampil "Partially reverted" bila job tersebut adalah revert. |
| `uncertain` | Hasil commit belum pasti dan worker akan memeriksa bukti eksekusi. |

Jenis aksi change request adalah `apply`, `rollback`, `forward`, `resume`, dan `revert`. Semuanya melewati review dan approval yang sama.

## Rollback, forward, dan migration history

Setiap migration punya `version` dan `is_applied`. Versi diberikan saat apply pertama berhasil, yaitu versi terbesar di database tersebut ditambah satu, dan versi tetap melekat walaupun migration di-rollback. Posisi aktif database adalah versi terbesar di antara migration dengan `is_applied = true`. Aturan posisi diperiksa saat change request dibuat dan diperiksa ulang di worker:

1. **Rollback** hanya untuk migration yang sedang aktif, versinya sama dengan posisi aktif teratas, dan tidak irreversible. Rollback menjalankan SQL down terhadap eksekusi sukses terakhir yang belum di-rollback, dan berhasil berarti `is_applied` menjadi false.
2. **Forward** hanya untuk migration yang pernah diterapkan, sedang tidak aktif, dan versinya adalah versi terkecil di atas posisi aktif. Forward menjalankan ulang SQL up dari revisi yang terakhir sukses.
3. **Migration baru** yang di-apply mendapat versi di atas semua versi yang ada, sehingga posisi aktif melompati migration yang sedang ter-rollback. Setelah itu migration ter-rollback yang versinya lebih rendah tidak bisa di-forward lagi, karena tidak ada lagi di atas posisi aktif.

Selama migration masih punya change request yang terbuka (`pending_review`, `approved`, `queued`, `running`, `partial`, `uncertain`), request rollback atau forward baru untuk migration itu ditolak 409.

Tabel `migration_history` mencatat setiap attempt eksekusi yang benar-benar dijalankan, baik sukses maupun gagal, tidak hanya hasil akhirnya. Setiap baris memuat `execution_id` (satu per job), `attempt` (mulai dari 1), `version`, `action` (`apply`, `rollback`, `forward`, atau `revert`, sedangkan resume dicatat dengan aksi asal yang dilanjutkannya), `status` (`success`, `failed`, `partial`), `reverts_execution_id` untuk rollback, `executed_sql`, `mode`, `batches`, `rows_affected`, `error_message`, `sqlstate`, `requested_by`, `started_at`, dan `finished_at`. Pasangan `(execution_id, attempt)` unik, dan sebuah unique index parsial menjamin hanya ada satu baris `success` per execution ID. Hanya attempt sukses yang menggeser posisi (`version`/`is_applied` di tabel `migrations`), sedangkan revert sukses tidak menggeser posisi. Revalidasi yang gagal tidak menghasilkan baris history karena SQL belum pernah dijalankan.

History bersifat immutable. Trigger `migration_history_immutable` menolak setiap `UPDATE` dan `DELETE` langsung dengan pesan "migration_history tidak boleh diubah atau dihapus". Pengecualiannya adalah `DELETE` yang datang dari cascade (misalnya koneksi database atau migration dihapus), yang dikenali dari `pg_trigger_depth() > 1`, agar penghapusan resource oleh admin tetap bisa berjalan.

Opsi **Hide rollback** (`GET /api/databases/{id}/history?hideRollback=true`) menyembunyikan pasangan eksekusi yang saling meniadakan. Pasangan ditentukan dari execution ID: setiap baris rollback berstatus `success` beserta `reverts_execution_id`-nya membentuk satu pasangan, lalu hanya baris `success` dari kedua execution ID itu yang disembunyikan. Attempt gagal dari eksekusi yang sama tetap tampil, sehingga jejak kegagalan tidak hilang.

Contoh riwayat lengkap dengan migration A, B, dan C (sesuai `TestHideRollbackExample`):

| Execution | Attempt | Aksi | Migration | Versi | Status | Membatalkan | Posisi aktif setelahnya |
|---|---|---|---|---|---|---|---|
| E1 | 1 | apply | A | 1 | success | | 1 |
| E2 | 1 | apply | B | 2 | success | | 2 |
| E3 | 1 | rollback | B | 2 | success | E2 | 1 |
| E4 | 1 | forward | B | 2 | success | | 2 |
| E5 | 1 | apply | C | 3 | success | | 3 |

Tampilan yang sama dengan Hide rollback aktif. E2 dan E3 hilang karena E3 adalah rollback sukses yang membatalkan E2:

| Execution | Attempt | Aksi | Migration | Versi | Status |
|---|---|---|---|---|---|
| E1 | 1 | apply | A | 1 | success |
| E4 | 1 | forward | B | 2 | success |
| E5 | 1 | apply | C | 3 | success |

Kalau E2 sempat gagal di attempt 1 dan E3 sempat gagal di attempt 1 sebelum keduanya sukses di attempt 2, Hide rollback tetap menampilkan `E2#1 failed` dan `E3#1 failed` dan hanya menyembunyikan attempt suksesnya (`TestHideRollbackKeepsFailures`).

## Asynchronous processing

### Queue dan API

Antrean berupa tabel `jobs` di database platform, tanpa message broker terpisah. `POST /api/change-requests/{id}/run` memasukkan satu baris job berstatus `queued` dengan `execution_id` UUID baru, menyimpan trace context request ke `jobs.trace_context`, mengubah change request menjadi `queued`, lalu membalas `202 Accepted`:

```json
{"jobId": 12, "executionId": "UUID eksekusi"}
```

Satu change request hanya bisa punya satu job (`change_request_id` unik), dan percobaan mengantrekan ulang ditolak 409. Endpoint rollback, forward, resume, dan revert hanya membuat change request baru (`201` dengan `changeRequestId` dan status `pending_review`) yang kemudian melewati approval dan Jalankan seperti apply.

Worker mengambil job dengan satu query `UPDATE ... FROM (SELECT ... FOR UPDATE SKIP LOCKED LIMIT 1)`. Kandidatnya adalah job `queued` atau `uncertain` yang `run_after`-nya sudah lewat, serta job `running` yang lease-nya habis (jalur recovery), diurutkan berdasarkan `run_after` lalu `id`. `SKIP LOCKED` membuat beberapa worker atau goroutine bisa claim bersamaan tanpa mendapat job yang sama (`TestClaimNoDuplicate`). Job yang di-claim langsung menjadi `running` dengan `worker_id` dan `lease_until`. Setiap proses worker menjalankan `WORKER_CONCURRENCY` loop claim paralel, dan saat antrean kosong setiap loop menunggu 1 detik sebelum mencoba lagi.

### Mode normal

Mode normal menjalankan SQL up (atau SQL down untuk rollback) dalam satu transaction di database target dengan simple protocol, sehingga beberapa statement dalam satu migration didukung. Worker menolak SQL yang mengandung perintah kontrol transaksi, men-set `standard_conforming_strings = on` agar cara membaca string sama dengan pemeriksa SQL, dan membatalkan eksekusi bila SQL ternyata menutup transaction sendiri. Di transaction yang sama worker menulis bukti eksekusi ke `_platform.executions` sebelum `COMMIT`. Karena bukti dan perubahan di-commit bersama, keberadaan baris bukti berarti SQL pasti sudah ter-commit, dan ketiadaannya berarti belum.

### Mode per batch

Mode batch ditujukan untuk perubahan data besar. SQL harus tepat satu statement yang memuat `{{lo}}` dan `{{hi}}`, misalnya `UPDATE orders_big SET price = price * 1.1 WHERE id > {{lo}} AND id <= {{hi}}`. Placeholder tidak disambung sebagai string, melainkan diganti menjadi parameter `$1` dan `$2` yang nilainya di-bind. Batas bawah bersifat eksklusif dan batas atas inklusif.

Batas setiap batch ditentukan dengan cursor pada kolom batch, bukan dengan offset. Pada eksekusi pertama, worker menghitung nilai maksimum kolom sekali dan menyimpannya di `_platform.batch_scopes`, sehingga row yang masuk setelah migration dimulai tidak ikut diproses dan cakupan tetap sama walaupun job dilanjutkan (`TestBatchScopeFixed`). Setiap iterasi mengambil nilai minimum dan maksimum dari `batch_size` nilai berikutnya di atas cursor, menjalankan SQL untuk rentang itu, lalu menulis checkpoint ke `_platform.batch_checkpoints` (execution ID, arah, nomor batch, lo, hi, jumlah row). Semuanya terjadi dalam satu transaction per batch, dan batch terakhir juga menulis bukti `_platform.executions` di transaction yang sama. Setelah setiap commit, `jobs.batches_committed` dan `jobs.rows_committed` diperbarui agar progres terlihat di UI. Schema `_platform` dibuat otomatis oleh worker di database target (`CREATE ... IF NOT EXISTS`) dan disembunyikan dari Database explorer (`TestViewerHidesPlatformSchemaIntegration`).

Batch yang sudah committed tetap tersimpan walaupun batch berikutnya gagal. Kalau error permanen terjadi setelah minimal satu batch committed, job berstatus **partial** dan dicatat di history dengan status `partial`. Job partial punya dua jalan keluar, keduanya lewat change request baru yang harus disetujui:

1. **Resume** (`POST /api/jobs/{id}/resume`) melanjutkan dari checkpoint terakhir dengan root execution ID yang sama, sehingga batch yang sudah committed tidak dijalankan ulang.
2. **Revert** (`POST /api/jobs/{id}/revert`) menjalankan SQL down hanya untuk batch yang sudah committed, dalam urutan terbalik (nomor batch terbesar lebih dulu), dengan checkpoint arah `down` tersendiri. Revert yang berhenti di tengah juga menjadi `partial` dan tampil sebagai **Partially reverted**, lalu bisa di-resume atau di-revert lagi.

Ketika resume atau revert selesai, job lain dalam keluarga eksekusi yang sama yang masih `partial` ikut ditutup: yang searah menjadi `success`, dan yang berlawanan arah menjadi `failed` dengan keterangan "Dibatalkan lewat revert". Setelah revert dimulai, melanjutkan ke arah up ditolak agar data tidak bolak-balik. Rollback migration batch yang sukses juga berjalan per batch, memakai checkpoint eksekusi yang di-rollback dalam urutan terbalik.

### Retry, uncertain, dan aturan tahan

Error dibagi menjadi sementara dan permanen. Error dianggap sementara (`queue.IsTransient`) bila termasuk salah satu kelompok berikut. `context.Canceled` tidak pernah dianggap sementara.

| Jenis | Kode |
|---|---|
| Gagal konek ke PostgreSQL | semua, kecuali SQLSTATE kelas `28` (autentikasi) dan `3D000` (database tidak ada) |
| Kelas SQLSTATE | `08` (connection exception), `53` (insufficient resources) |
| SQLSTATE spesifik | `57P03` (cannot connect now), `40001` (serialization failure), `40P01` (deadlock), `55P03` (lock not available), `57P01` (admin shutdown), `57014` (query canceled, termasuk statement timeout) |
| Error non-PostgreSQL | `net.Error`, `io.EOF`, `io.ErrUnexpectedEOF`, error yang ditandai aman di-retry oleh pgconn, timeout pgconn, `context.DeadlineExceeded` |

Error sementara di database target membuat attempt dicatat `failed` di history lalu job dikembalikan ke `queued` dengan jeda 2, 4, 8, lalu 16 detik (maksimal 16 detik) sebelum attempt berikutnya. Batasnya adalah `jobs.max_attempts`, default 5. Setelah attempt terakhir, job menjadi `failed`, atau `partial` bila sudah ada batch committed, dengan pesan "Gagal setelah N percobaan". Retry mode batch otomatis melanjutkan dari checkpoint. Error permanen langsung final tanpa retry. Gangguan sementara di database platform sebelum eksekusi (misalnya saat revalidasi) tidak menambah attempt dan job hanya dikembalikan ke antrean.

Status **uncertain** dipakai ketika hasil commit di target tidak bisa dipastikan. Contohnya `COMMIT` yang terputus, SQL yang sudah commit tetapi gagal dicatat ke history, bukti eksekusi yang ternyata sudah ditulis eksekutor lain, atau job yang ditinggal worker mati di tengah jalan. Job uncertain diambil lagi oleh worker setelah jeda backoff. Worker tidak menjalankan ulang SQL secara buta, melainkan memeriksa bukti eksekusi lebih dulu: bila `_platform.executions` sudah berisi execution ID dan arah tersebut, job dicatat `success` tanpa eksekusi ulang (`TestDuplicateNoDoubleCommit`, `TestRecoverUncertain`). Bila belum ada, SQL dijalankan seperti biasa karena transaction sebelumnya terbukti tidak ter-commit. Untuk batch, pencocokan dilakukan terhadap checkpoint. Kalau attempt sudah mencapai batas dan hasilnya tetap belum pasti, job dicoba setiap satu jam dan lognya ditandai `needs_manual_check`.

Selama sebuah database punya job yang hasilnya belum beres, job lain di database itu ditahan. Yang termasuk adalah job `partial`, job `uncertain`, job `running` yang lease-nya habis, dan job `queued` yang sudah punya batch committed (dihitung bila attempt-nya sudah lebih dari 0, kecuali `partial` yang selalu dihitung). Job dalam satu keluarga eksekusi (job asal beserta resume/revert-nya) tidak saling menahan. Job yang tertahan dikembalikan ke `queued` setiap 2 detik dengan log `job.held`, sampai penahannya selesai di-resume, di-revert, atau terbukti hasilnya.

### Satu migration per database

Lock disimpan di tabel `database_locks` dengan primary key `db_connection_id`, sehingga paling banyak satu job memegang lock per database. Database yang berbeda tetap diproses paralel (`TestSameDBSerialDiffDBParallel`). Lock punya lease 30 detik. Selama job berjalan, goroutine heartbeat memperpanjang lease lock dan lease job setiap 10 detik. Kalau lock sedang dipegang job lain dengan lease yang masih berlaku, job yang datang dikembalikan ke antrean dengan jeda 2 detik (`lock.busy`). Kalau heartbeat mendapati lock sudah bukan miliknya, atau perpanjangan terus gagal sampai sisa lease tinggal satu interval heartbeat (20 detik sejak perpanjangan terakhir yang berhasil), context job dibatalkan (`lock.lost`). Dengan begitu worker yang lock-nya bisa diambil alih tidak melanjutkan eksekusi.

### Pemulihan lock ketika worker gagal

Kalau proses worker mati (crash, kill, atau mesin mati) saat memegang job, baris job tertinggal berstatus `running` dan baris lock tertinggal di `database_locks`. Keduanya punya `lease_until` yang berhenti diperpanjang. Pemulihannya berjalan otomatis:

1. Setelah lease habis (paling lama 30 detik setelah heartbeat terakhir), job `running` itu masuk kandidat claim dan bisa diambil worker mana pun, dan lock yang lease-nya habis boleh diambil alih.
2. Saat worker lain mengambil alih lock milik job yang berbeda, job pemegang lama diperiksa. Bila job lama belum pernah menyentuh target (attempt 0), job itu cukup dikembalikan ke `queued`. Bila sudah, job lama ditandai `uncertain` dengan pesan "Worker sebelumnya berhenti di tengah jalan, hasil perlu dicek ulang". Worker yang mengambil alih lalu melepas lock dan mengantre ulang job-nya sendiri, sehingga job uncertain diselesaikan lebih dulu (`TestLockTakeover`, `TestLockTakeoverNotStarted`).
3. Job uncertain atau job yang di-claim ulang dari status `running` diproses dalam mode hati-hati. Revalidasi dilewati, lalu bukti di `_platform.executions` atau checkpoint batch menentukan apakah SQL sudah ter-commit. Job dicatat sukses tanpa eksekusi ulang bila buktinya ada, atau dijalankan bila buktinya tidak ada.
4. Worker lama yang ternyata masih hidup (misalnya sempat hang) tidak bisa merusak hasil. Semua pembaruan status job memakai syarat `worker_id` dan `status = 'running'`, sehingga pembaruan dari worker lama ditolak dan dicatat `job.lost`. Primary key `_platform.executions` mencegah dua bukti untuk eksekusi yang sama, dan bentrok `23505` di sana diperlakukan sebagai uncertain, bukan sukses ganda.
5. Kalau penulisan status akhir ke database platform gagal, lock sengaja tidak dilepas agar lease habis dan job masuk jalur recovery yang sama. Saat worker menerima SIGINT atau SIGTERM, job yang sedang berjalan dibatalkan dan dibiarkan `running`, lalu dipulihkan dengan cara yang sama.

## Ketentuan pengujian dataset besar

Spesifikasi meminta dataset yang cukup besar sehingga UPDATE tanpa batch memakan waktu lebih dari 1 detik. Dataset dibuat dengan `go run ./cmd/seed-bigdata` (default 3.000.000 row di `orders_big` pada `target-db`), dan durasi UPDATE tanpa batch diukur dengan `go run ./cmd/seed-bigdata -measure-only`. Hasil pengukuran dari log `seed.measured`:

| Ukuran | Nilai |
|---|---|
| Jumlah row `orders_big` | 3.000.000 |
| Durasi `UPDATE orders_big SET price = price * 1.1` tanpa batch | 7.195 ms (`duration_ms`), atau 7,196 detik pada atribut `duration` yang dibulatkan ke milidetik terdekat |
| Mesin pengukuran | MacBook Pro Apple Silicon (arm64), PostgreSQL 16 di Docker Desktop |

Durasi tersebut jauh di atas batas 1 detik, sehingga dataset ini memenuhi ketentuan. Migration batch untuk dataset ini memakai tabel `orders_big`, kolom `id`, dan SQL seperti `UPDATE orders_big SET price = price * 1.1 WHERE id > {{lo}} AND id <= {{hi}}` dengan SQL down `UPDATE orders_big SET price = price / 1.1 WHERE id > {{lo}} AND id <= {{hi}}`.

## Activity logging dan tracing

Server dan worker menulis log sebagai JSON satu baris per event ke stdout lewat `log/slog` yang dikonfigurasi di `internal/obs`. Kunci pesan bawaan slog (`msg`) diganti menjadi `event`, dan setiap baris diberi atribut `service` (`server` atau `worker`). Logger diambil dari context, sehingga atribut yang dipasang di awal (misalnya ID job) ikut terbawa ke semua log di bawahnya, termasuk log query SQL. Bila context membawa span aktif, `trace_id` dan `span_id` ditambahkan otomatis. `cmd/seed-bigdata` tidak memakai `internal/obs`: program itu membuat logger JSON slog sendiri dengan atribut `service` bernilai `seed-bigdata`, sehingga nama event-nya tetap berada di kunci `msg` dan tidak ada `trace_id`.

| Atribut | Isi |
|---|---|
| `time`, `level`, `event`, `service` | Ada di setiap baris server dan worker. |
| `trace_id`, `span_id` | ID trace dan span OpenTelemetry aktif. |
| `actor` | Username pengaju (log worker dan `change_request.created`). |
| `database` | ID koneksi database target. |
| `status` | Hasil event, misalnya `ok`, `error`, `running`, `success`, `failed`, `busy`, atau status HTTP untuk `http.request`. |
| `query`, `params` | SQL yang sudah disamarkan dan parameter yang sudah disaring. |
| `duration_ms`, `rows` | Durasi dalam milidetik dan jumlah row terdampak. |
| `sqlstate`, `error` | Kode SQLSTATE dan pesan error yang sudah disamarkan. |
| `job_id`, `execution_id`, `migration_id`, `attempt`, `batch`, `worker_id`, `change_request_id` | Identitas pekerjaan. Di dalam eksekusi, `execution_id` adalah root execution ID yang dipakai checkpoint, sedangkan execution ID job ada di `job_execution_id` pada `job.started`. |
| `method`, `path` | Untuk `http.request`. |

Daftar nama event:

| Kelompok | Event |
|---|---|
| HTTP dan server | `http.request`, `server.started`, `server.stopped`, `server.db_connect_failed`, `server.seed_admin_failed`, `server.manager_failed`, `server.resource_handler_failed`, `obs.setup_failed` |
| SQL | `sql.start`, `sql.result`, `sql.begin`, `sql.commit`, `sql.rollback` |
| Workflow | `change_request.created` |
| Worker | `worker.started`, `worker.stopped`, `worker.bad_concurrency`, `worker.metrics_failed`, `worker.claim_failed`, `worker.db_connect_failed`, `worker.manager_failed` |
| Job | `job.claimed`, `job.claim_failed`, `job.held`, `job.hold_check_failed`, `job.duplicate`, `job.plan_failed`, `job.target_check_retry`, `job.start_failed`, `job.started`, `job.retry_scheduled`, `job.uncertain`, `job.finished`, `job.aborted`, `job.lost`, `job.finalize_failed`, `job.requeue_failed`, `job.progress_failed` |
| Revalidasi | `revalidation.retry`, `revalidation.failed` |
| Lock | `lock.acquired`, `lock.busy`, `lock.recovered`, `lock.failed`, `lock.released`, `lock.release_failed`, `lock.heartbeat_failed`, `lock.lost` |
| Batch dan history | `batch.committed`, `history.record_failed` |
| Metrik | `metrics.query_failed` |
| Seed (kunci `msg`) | `seed.invalid_args`, `seed.failed`, `seed.reset`, `seed.skipped`, `seed.chunk_inserted`, `seed.inserted`, `seed.measure_started`, `seed.measured`, `seed.too_fast` |

Paket auth, admin, dbmanager, dan migration tidak menulis event sendiri. Aktivitasnya terekam lewat `http.request` dan event SQL dari tracer pgx.

Log query ditulis oleh tracer pgx (`internal/obs/pgx.go`) yang terpasang di setiap pool platform. Event mulai, hasil, commit, dan rollback dipisah: untuk query biasa, `sql.start` ditulis saat query dimulai dan `sql.result` saat selesai, sedangkan `BEGIN`, `COMMIT`, dan `ROLLBACK` masing-masing menghasilkan `sql.begin`, `sql.commit`, dan `sql.rollback`. Query yang gagal dicatat dengan level error, `status: "error"`, `sqlstate`, dan pesan error.

Penyamaran data berlaku untuk semua log dan span. Teks SQL dan pesan error dilewatkan ke `obs.Redact`, yang mengganti nilai `password '...'`, `password=...`, dan password di connection string `scheme://user:password@host` menjadi `[REDACTED]`. Parameter query hanya ditampilkan apa adanya bila bertipe `nil`, boolean, integer, atau float, dan semua parameter lain termasuk string, byte, dan waktu ditulis `[REDACTED]`. Karena session ID dan token CSRF hanya dikirim ke database sebagai parameter string (session disimpan dalam bentuk hash), nilainya tidak pernah muncul di log. Log `http.request` hanya memuat method, path, status, dan durasi, tanpa header, cookie, atau body.

Tracing memakai OpenTelemetry dengan propagator W3C TraceContext. Rantai span untuk satu eksekusi:

```
POST /api/change-requests/{id}/run      (span HTTP dari otelhttp, nama memakai path asli)
  db.begin / db.query / db.commit       (query di server, termasuk INSERT INTO jobs)
        |
        |  trace context disimpan di jobs.trace_context
        v
worker.job                              (worker, parent = span HTTP di atas)
  lock.acquire
  revalidate
  db.query ...                          (perencanaan, status job, dan query di target)
  batch                                 (satu span per batch, atribut batch, lo, hi)
    db.query / db.commit
```

Saat menjalankan change request, server menyuntikkan trace context ke `jobs.trace_context`. Worker mengekstraknya sebelum membuat span `worker.job`, sehingga span worker berada dalam trace yang sama dengan request HTTP yang memicunya walaupun berjalan di proses lain dan bisa beberapa detik kemudian. Tidak ada span khusus bernama queue: mata rantai antrean adalah span `db.query` untuk `INSERT INTO jobs` ditambah `jobs.trace_context`. Query claim di worker terjadi sebelum ekstraksi itu sehingga tidak masuk ke trace tersebut. Span query memakai nama `db.query`, `db.begin`, `db.commit`, atau `db.rollback` dengan atribut `db.system`, `db.statement` (sudah disamarkan), `db.rows_affected`, dan `db.sqlstate` bila gagal.

Exporter yang dipakai adalah exporter console OpenTelemetry (`stdouttrace`) yang menulis span ke **stderr**, satu objek JSON per span, dengan batch setiap 1 detik. Karena log ke stdout dan span ke stderr, keduanya mudah dipisah:

```
go run ./cmd/server 2> spans.json
go run ./cmd/worker 2> worker-spans.json
```

### Contoh output

Contoh berikut diambil dari server lokal untuk satu request `GET /api/databases` setelah login. Baris log `sql.result` untuk query pembaruan aktivitas session (parameter waktu disamarkan, ID session numerik tetap terlihat):

```json
{"time":"2026-11-26T19:02:12.262701+07:00","level":"INFO","event":"sql.result","service":"server","trace_id":"01cba78b939cb7c087280489151edc3f","span_id":"9744e1c7934de5b5","query":"UPDATE sessions SET last_activity_at = $1 WHERE id = $2","params":["[REDACTED]",7],"duration_ms":2.078,"rows":1,"status":"ok"}
```

Baris log `http.request` untuk request yang sama:

```json
{"time":"2026-11-26T19:02:12.278748+07:00","level":"INFO","event":"http.request","service":"server","trace_id":"01cba78b939cb7c087280489151edc3f","span_id":"012a5ef34a845b40","method":"GET","path":"/api/databases","status":200,"duration_ms":22.293}
```

Span `db.query` untuk query tersebut, dari stderr:

```json
{"Name":"db.query","SpanContext":{"TraceID":"01cba78b939cb7c087280489151edc3f","SpanID":"9744e1c7934de5b5","TraceFlags":"01","TraceState":"","Remote":false},"Parent":{"TraceID":"01cba78b939cb7c087280489151edc3f","SpanID":"012a5ef34a845b40","TraceFlags":"01","TraceState":"","Remote":false},"SpanKind":3,"StartTime":"2026-11-26T19:02:12.260617+07:00","EndTime":"2026-11-26T19:02:12.2627115+07:00","Attributes":[{"Key":"db.system","Value":{"Type":"STRING","Value":"postgresql"}},{"Key":"db.statement","Value":{"Type":"STRING","Value":"UPDATE sessions SET last_activity_at = $1 WHERE id = $2"}},{"Key":"db.rows_affected","Value":{"Type":"INT64","Value":1}}],"Events":null,"Links":null,"Status":{"Code":"Unset","Description":""},"DroppedAttributes":0,"DroppedEvents":0,"DroppedLinks":0,"ChildSpanCount":0,"Resource":[{"Key":"service.name","Value":{"Type":"STRING","Value":"server"}}],"InstrumentationScope":{"Name":"github.com/fahdmz/K03-05-WBD","Version":"","SchemaURL":"","Attributes":null},"InstrumentationLibrary":{"Name":"github.com/fahdmz/K03-05-WBD","Version":"","SchemaURL":"","Attributes":null}}
```

Span HTTP parent-nya:

```json
{"Name":"GET /api/databases","SpanContext":{"TraceID":"01cba78b939cb7c087280489151edc3f","SpanID":"012a5ef34a845b40","TraceFlags":"01","TraceState":"","Remote":false},"Parent":{"TraceID":"00000000000000000000000000000000","SpanID":"0000000000000000","TraceFlags":"00","TraceState":"","Remote":false},"SpanKind":2,"StartTime":"2026-11-26T19:02:12.256447+07:00","EndTime":"2026-11-26T19:02:12.278765875+07:00","Attributes":[{"Key":"server.address","Value":{"Type":"STRING","Value":"localhost"}},{"Key":"http.request.method","Value":{"Type":"STRING","Value":"GET"}},{"Key":"url.scheme","Value":{"Type":"STRING","Value":"http"}},{"Key":"server.port","Value":{"Type":"INT64","Value":8080}},{"Key":"network.peer.address","Value":{"Type":"STRING","Value":"::1"}},{"Key":"network.peer.port","Value":{"Type":"INT64","Value":54882}},{"Key":"user_agent.original","Value":{"Type":"STRING","Value":"curl/8.7.1"}},{"Key":"client.address","Value":{"Type":"STRING","Value":"::1"}},{"Key":"url.path","Value":{"Type":"STRING","Value":"/api/databases"}},{"Key":"network.protocol.version","Value":{"Type":"STRING","Value":"1.1"}},{"Key":"http.route","Value":{"Type":"STRING","Value":"/"}},{"Key":"http.response.body.size","Value":{"Type":"INT64","Value":40}},{"Key":"http.response.status_code","Value":{"Type":"INT64","Value":200}}],"Events":null,"Links":null,"Status":{"Code":"Unset","Description":""},"DroppedAttributes":0,"DroppedEvents":0,"DroppedLinks":0,"ChildSpanCount":4,"Resource":[{"Key":"service.name","Value":{"Type":"STRING","Value":"server"}}],"InstrumentationScope":{"Name":"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp","Version":"0.72.0","SchemaURL":"","Attributes":null},"InstrumentationLibrary":{"Name":"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp","Version":"0.72.0","SchemaURL":"","Attributes":null}}
```

Keempat baris memakai `trace_id` yang sama, `01cba78b939cb7c087280489151edc3f`. `span_id` pada log `sql.result` sama dengan `SpanID` span `db.query`, dan `span_id` pada log `http.request` sama dengan `SpanID` span HTTP, yang juga menjadi `Parent.SpanID` dari span `db.query`. Span HTTP itu punya empat child (`ChildSpanCount: 4`), yaitu empat query yang dijalankan selama request: pemeriksaan session, pembaruan aktivitas session, daftar koneksi, dan pemeriksaan akses efektif.

## Metrik

Server dan worker sama-sama menyediakan endpoint `/metrics` berformat teks Prometheus, dibangun dengan exporter kecil di `internal/metrics` tanpa dependency tambahan. Endpoint server ada di http://localhost:8080/metrics, dipasang di luar middleware HTTP agar scrape tidak membanjiri log, dan tidak memerlukan login. Endpoint worker ada di http://localhost:9091/metrics atau alamat dari `WORKER_METRICS_ADDR`.

| Metrik | Tipe | Label | Proses |
|---|---|---|---|
| `server_db_pool_connections` | gauge | `state` (`active`, `idle`, `total`, `max`) | server |
| `server_db_pool_acquire_wait_seconds_total` | counter | | server |
| `worker_jobs_finished_total` | counter | `status` (`success`, `failed`, `partial`, `uncertain`, `aborted`), `action`, `mode` | worker |
| `worker_job_retries_total` | counter | `action`, `mode` | worker |
| `worker_migration_duration_seconds` | histogram | `status`, `action`, `mode` | worker |
| `worker_queue_wait_seconds` | histogram | | worker |
| `worker_lock_wait_seconds` | histogram | `result` (`acquired`, `busy`, `recovered`, `error`) | worker |
| `worker_batches_committed_total` | counter | `direction` (`up`, `down`) | worker |
| `worker_rows_committed_total` | counter | `direction` | worker |
| `worker_jobs` | gauge | `status` (`queued`, `running`, `success`, `failed`, `partial`, `uncertain`) | worker |
| `worker_db_pool_connections` | gauge | `state` (`active`, `idle`, `max`) | worker |
| `worker_db_pool_acquire_wait_seconds_total` | counter | | worker |

Label `action` berisi `apply`, `rollback`, `forward`, `resume`, `revert`, atau `unknown`, dan `mode` berisi `normal` atau `batch`. Bucket histogram durasi adalah 0,05; 0,1; 0,25; 0,5; 1; 2,5; 5; 10; 30; 60; 300; dan 900 detik. `worker_queue_wait_seconds` hanya diamati pada claim pertama sebuah job, dan `worker_jobs` dihitung dari tabel `jobs` setiap kali di-scrape.

Label sengaja dibatasi pada nilai yang jumlahnya kecil dan tetap. ID job, ID database, execution ID, username, dan teks SQL tidak dijadikan label, karena setiap nilai label unik menghasilkan time series baru sehingga memori Prometheus membengkak tanpa batas, dan karena SQL atau nama pengguna bisa memuat data sensitif. Detail per eksekusi tersedia di log dan trace, yang memang dirancang untuk data berkardinalitas tinggi.

## Automated testing

Pastikan `test-db` berjalan, lalu jalankan dari root repository:

```
docker compose up -d test-db
go test ./... -count=1
```

Untuk menunjuk database lain, set `TEST_DATABASE_URL` (default `postgres://test:test@localhost:5434/test?sslmode=disable`). Beberapa paket juga menerima override terpisah: `POSTGRES_MANAGER_TEST_DATABASE_URL`, `POSTGRES_MANAGER_TEST_TARGET_URL`, `VIEWER_TEST_DATABASE_URL`, `VIEWER_TEST_TARGET_URL`, `MIGRATION_TEST_DATABASE_URL`, dan `MIGRATION_TEST_TARGET_URL`. Test integrasi membuat schema sendiri (misalnya `history_test`, `workflow_test_...`, atau `wk_...` dengan akhiran acak), menerapkan `001_init.sql` dan `002_m2.sql` dari folder `migrations/`, lalu menghapusnya setelah selesai. Test worker juga membuat database target sementara dengan `CREATE DATABASE`. Kalau `test-db` tidak bisa dihubungi, test integrasi ditandai SKIP dan tidak gagal, jadi periksa output `-v` bila jumlah test yang berjalan terlihat terlalu sedikit.

Frontend: `cd frontend && npm run build && npm run lint`.

Ringkasan cakupan per paket. Hampir setiap paket menguji jalur berhasil, jalur gagal, dan penolakan akses.

| Paket | Yang diuji |
|---|---|
| `internal/auth` | Login berhasil dan gagal, cookie aman, `/me` butuh login, CSRF dan logout, idle timeout, absolute expiration, rotasi dan grace period, revoke dan rotate session, user nonaktif ditolak, session ikut transaksi. |
| `internal/authz` | Akses efektif dari beberapa group, `Require` menolak level kurang, level minimum tidak valid, integrasi dengan database. |
| `internal/admin` | CRUD user, group, koneksi, anggota, akses, dan leader, perubahan session saat privilege berubah, rollback bila perubahan session gagal, endpoint admin menolak non-admin, validasi input sebelum menyentuh database, enkripsi password koneksi. |
| `internal/dbmanager` | Dekripsi password dan penolakan data terpotong, quoting kredensial, validasi key, error koneksi tidak membocorkan password, pagination, viewer menolak akses dan kegagalan target, schema `_platform` disembunyikan. |
| `internal/migration` | Decode input, validasi struktur, alur migration terintegrasi, execution plan (penolakan akses, error, integrasi). |
| `internal/workflow` | Unit: request tanpa login ditolak 401, ID path tidak valid 400, reject tanpa komentar 400, filter status tidak valid 400, komentar lebih dari 2000 karakter 400. Integrasi: anggota bukan leader dan leader group tanpa akses ditolak 403, run sebelum approved ditolak 400, daftar review leader, approval satu dari dua group tetap `pending_review` lalu dua dari dua menjadi `approved`, group baru diberi akses membuat run ditolak 409 sampai leadernya menyetujui, user View ditolak 403 saat run, run berhasil 202 dengan `jobId` dan `executionId`, run kedua ditolak 409, revalidasi lolos lalu gagal dengan menyebut group yang kehilangan leader, satu reject membuat status `rejected` dan run ditolak 400, serta rantai resume bertingkat lolos revalidasi. |
| `internal/history` | Hide rollback (contoh E1 sampai E5 dan attempt gagal tetap tampil), alur rollback dan forward, aturan posisi, attempt gagal tercatat, history immutable, endpoint rollback dan job termasuk penolakan akses, aksi efektif job. |
| `internal/queue` | Klasifikasi error sementara, ekstraksi SQLSTATE, backoff. |
| `internal/worker` | Apply normal, error permanen, batas retry, tolak SQL kontrol transaksi, claim tanpa duplikat, serial per database dan paralel antar database, tidak commit dua kali, recovery uncertain, pengambilalihan lock (sudah dan belum menyentuh target), lock sibuk, tahan dan resume, revalidasi gagal, rollback, lock hilang membatalkan job. Batch: cakupan tetap, resume setelah crash, duplikat, batch pertama gagal, retry sementara, revert urutan terbalik, penjaga revert setelah partial, revert rollback, tolak SQL. End to end: apply dan rollback batch, partial lalu resume, revert partial, metrik, rollback setelah resume. |
| `internal/sqlcheck` | Pemecahan statement, deteksi kontrol transaksi, placeholder batch. |
| `internal/obs` | Redaksi, penyaringan parameter, penamaan event SQL dan span, kunci `event`, propagasi trace, `trace_id` di log. |
| `internal/metrics`, `cmd/server` | Format eksposisi Prometheus, escape label, handler `/metrics` server. |
| `cmd/seed-admin`, `cmd/seed-bigdata` | Validasi argumen dan integrasi dengan database. |

## Bonus

### Execution plan

User dengan akses Edit dapat memilih **Lihat execution plan** pada editor SQL atau detail migration mode normal. Endpoint `POST /api/databases/{id}/explain` menerima `{"sql":"SELECT ..."}` dan memerlukan session serta header CSRF seperti endpoint POST lainnya.

Hasil menampilkan pohon operasi, scan, index, jenis join, estimated cost, dan estimated rows. Cost merupakan estimasi planner, bukan waktu dalam milidetik. JSON lengkap tersedia untuk inspeksi. Hasil dibersihkan ketika SQL diganti agar plan lama tidak dianggap berlaku untuk query baru.

Analisis memakai `EXPLAIN (FORMAT JSON, ANALYZE FALSE)` untuk satu statement. Input multi-statement ditolak melalui extended protocol PostgreSQL. SELECT, INSERT, UPDATE, dan DELETE dapat dianalisis, sedangkan CREATE TABLE dan ALTER TABLE tidak didukung. Tidak ada actual time atau actual rows karena ANALYZE tidak digunakan. Transaksi target read-only, statement timeout 5 detik, lock timeout 2 detik, dan context timeout 8 detik membatasi proses analisis. EXPLAIN tidak membuat versi, change request, atau riwayat migration.

### CodeQL

Workflow `.github/workflows/codeql.yml` menjalankan analisis statis CodeQL dengan query suite `security-extended` untuk dua bahasa dalam matrix terpisah: Go dengan build mode `autobuild` dan JavaScript/TypeScript dengan build mode `none`. Workflow berjalan pada push ke `main`, `dev`, `feat/dev`, dan `feat/m2-workflow-approval`, pada pull request ke `main`, `dev`, dan `feat/dev`, setiap Senin pukul 01.30 UTC, dan bisa dipicu manual lewat `workflow_dispatch`. Setiap bahasa dianalisis dengan kategori `/language:<bahasa>`, dan hasilnya diunggah ke tab Security GitHub berkat permission `security-events: write`. Permission lain hanya baca (`contents`, `actions`, `packages`).

## Format error

Semua error dikembalikan dalam format `{"error": "pesan"}`, dengan status 400 (input tidak valid atau status tidak memungkinkan aksi), 401 (belum login), 403 (tidak punya akses, bukan leader, atau CSRF salah), 404, 409 (konflik, misalnya aturan posisi, change request terbuka, atau sudah diantrekan), 500, dan 502 (koneksi ke database target gagal). Endpoint Jalankan membalas 202 karena eksekusi berlangsung di worker.

## Struktur repo

```
cmd/server/                 entry point API dan /metrics server
cmd/worker/                 entry point worker dan /metrics worker
cmd/seed-admin/             command pembuat akun admin
cmd/seed-bigdata/           seed dan pengukuran dataset besar
internal/auth/              login, session, CSRF
internal/authz/             hak akses dan akses efektif
internal/admin/             endpoint admin (user, group, leader, koneksi, akses)
internal/dbmanager/         koneksi database target dan viewer
internal/migration/         migration, revisi, validasi, submit, execution plan
internal/workflow/          change request, approval, revalidasi
internal/history/           migration history, rollback, forward, endpoint job
internal/queue/             enqueue, claim, requeue, klasifikasi retry
internal/worker/            eksekutor, lock, batch, recovery, metrik worker
internal/sqlcheck/          pemecah statement dan placeholder batch
internal/obs/               log JSON, tracing, tracer pgx, redaksi
internal/metrics/           exporter teks Prometheus
internal/store/             koneksi pool platform
internal/web/               helper JSON response
migrations/001_init.sql     schema database internal M1
migrations/002_m2.sql       schema tambahan M2 dan konversi data M1
frontend/src/components/    komponen bersama (layout, tabel, guard)
frontend/src/features/      halaman per fitur (admin, auth, databases, history, jobs, reviews)
frontend/src/lib/           klien API, auth, format, toast
.github/workflows/codeql.yml  analisis CodeQL
```

## Anggota

1. Fahd Muhammad Zahid - 13524078
2. Nathanael Gunawan - 13524066
3. Mahatma Brahmana - 13524015
