# Architecture Decision Records (DECISIONS.md)

Dokumen ini mencatat keputusan arsitektur utama dan kompromi (*trade-off*) untuk proyek **Treean (`github.com/Torikh42/treean`)**.

---

## Daftar Keputusan

| ID | Topik | Pilihan | Alternatif yang Ditolak |
| :--- | :--- | :--- | :--- |
| ADR-001 | Bahasa & Stack Web | Go 1.26 + `chi/v5` + `Huma v2` + `pgx/v5` | Node/Bun + Hono, atau ORM (GORM/ent) |
| ADR-002 | Arsitektur Kode | Clean Layered + DDD-Lite (`domain`, `handler`, `service`, `repository`) | MVC tradisional, Full DDD multi-context |
| ADR-003 | Pencegahan Oversell & Deadlock | PostgreSQL Pessimistic Lock (`FOR UPDATE` terurut `ASC`) + `CHECK` Constraints | Optimistic Locking (`version`), Atomic Update tanpa lock multi-item |
| ADR-004 | Batas Maks 4 Tiket/Pembeli/Acara | `pg_advisory_xact_lock(hashtext(event), hashtext(buyer))` | `SELECT SUM()` biasa, atau `SERIALIZABLE` penuh seluruh tabel |
| ADR-005 | Auto-Expire 15 Menit | Hybrid: Background Ticker (`FOR UPDATE SKIP LOCKED`) + Partial-Index Lazy Check | Ticker periodik saja, atau Redis + Delayed Queue |
| ADR-006 | Webhook Idempotency & Late Payment | `payment_webhooks` log di luar tx + `orders FOR UPDATE` + **Quota Safety First** | Pola "PAID Always Wins", atau in-memory idempotency cache |
| ADR-007 | Penomoran Invoice & Tiket | PostgreSQL `SEQUENCE` (`invoice_seq`) + `crypto/rand` + `UNIQUE(order_item_id, seat_sequence)` | `SELECT MAX(...) FOR UPDATE` pada tabel `orders` |
| ADR-008 | Identitas Demo & Uji Konkurensi | Hybrid JWT Bearer + Opt-in Demo Headers (`ALLOW_DEMO_HEADER_AUTH`) | Strict JWT saja untuk skenario benchmark lokal |
| ADR-009 | Gateway Tunggal Nginx, Redis Fail-Open & Checkout Idempotency | Single-entry-point Nginx (`limit_req`, security headers, isolasi port + `db-debug`) + Redis `fail-open` (`user_id` rate limit & cache 3s) + DB `Idempotency-Key` | Redis queue/session penuh, IP-only rate limit, atau port DB/API terbuka ke host |

---

## ADR-001: Go 1.26 + `chi/v5` + `Huma v2` + `pgx/v5` Tanpa ORM

- **Konteks:** Studi kasus menuntut penanganan konkurensi tinggi (200 *concurrent booking requests*), keamanan transaksi database, validasi ketat, dan dokumentasi API interaktif untuk live demo 5 menit.
- **Keputusan:** Menggunakan `pgx/v5` (`pgxpool`) dengan SQL eksplisit dan `Huma v2` di atas `go-chi/chi/v5` lengkap dengan Scalar UI (`/docs`).
- **Alasan:**
  1. **Kontrol Penuh Locking SQL:** Dengan `pgx/v5`, urutan akuisisi row lock (`ORDER BY id ASC FOR UPDATE`, `SKIP LOCKED`, `pg_advisory_xact_lock`) eksplisit dan mudah diaudit tanpa query tersembunyi dari ORM.
  2. **Single Source of Truth OpenAPI 3.1:** `Huma v2` memvalidasi struct tag Go (`minItems`, `maximum`, `minimum`, `format:"uuid"`) sekaligus menghasilkan `/openapi.json` dan UI interaktif Scalar secara otomatis, menyamai DX `@hono/zod-openapi` di ekosistem TypeScript tanpa skrip generator eksternal.
- **Trade-Off:** Query SQL dan struct scanning (`pgx.CollectRows`) ditulis manual lebih banyak dibanding ORM, tetapi biaya pengembangannya kecil untuk 7 tabel inti dan sangat aman terhadap N+1 atau deadlock tak terduga.

---

## ADR-002: Clean Layered + DDD-Lite (`internal/domain` Murni)

- **Konteks:** Proyek berdurasi 8 hari dengan sekitar 2.000-4.000 baris kode Go dalam 1 domain kohesif (Ticketing).
- **Keputusan:** Struktur folder horizontal (`handler`, `service`, `repository`, `worker`) yang bergantung ke package `internal/domain` murni (tanpa import SQL/HTTP). Interface repository didefinisikan oleh consumer (`internal/service`).
- **Alternatif yang Ditolak:** Full DDD dengan Bounded Context terpisah (`event/`, `order/`, `payment/`, `ticket/`) atau MVC.
- **Alasan & Trade-Off:**
  - MVC cenderung mencampur logika transaksi ke Controller atau query ke Model; tidak sesuai dengan filosofi Go REST API.
  - Full DDD menambah boilerplate konversi entity-ke-DTO lintas folder untuk transaksi yang justru harus menyentuh `orders`, `order_items`, dan `ticket_types` dalam satu transaksi DB.
  - Dengan menempatkan state machine (`Order.CanPay()`, `Order.CanCancel()`) dan konstanta (`MaxTicketsPerBuyerPerEvent = 4`) di `internal/domain`, aturan bisnis bisa diuji sebagai pure unit test (`< 1ms`) tanpa menyalakan database.

---

## ADR-003: Pessimistic Row Locking (`FOR UPDATE` Terurut) untuk Zero Oversell & Multi-Item Order

- **Konteks:** Pembeli dapat memesan campuran beberapa jenis tiket dalam 1 pesanan (`orders` + `order_items`), dan 200 pembeli dapat memesan kuota 100 tiket secara bersamaan.
- **Keputusan:** Mengunci seluruh baris `ticket_types` yang terlibat menggunakan `SELECT ... FROM ticket_types WHERE id = ANY($1) AND event_id = $2 ORDER BY id ASC FOR UPDATE` sebelum mengurangi `remaining_quota`, didukung constraint database `CHECK (remaining_quota >= 0 AND remaining_quota <= total_quota)`.
- **Mengapa Tidak Optimistic Locking (`version` column)?**
  Pada flash sale di mana 200 goroutine memperebutkan baris `ticket_types` yang sama di milidetik yang sama, 199 goroutine akan mengalami conflict `version` pada percobaan pertama meski kuota sebenarnya masih tersisa 99. Client harus melakukan *retry storm* yang memperburuk latensi. Dengan `FOR UPDATE`, PostgreSQL mengantrekan 200 transaksi secara adil dan deterministik: tepat 100 sukses dan 100 mendapat `409 QUOTA_EXHAUSTED`.
- **Pencegahan Deadlock (Lock Ordering Invariant):**
  Jika Pembeli A memesan `[VIP, Reguler]` dan Pembeli B memesan `[Reguler, VIP]`, penguncian tanpa pengurutan berisiko memicu deadlock (`40P01`). Dengan selalu mengurutkan `ticket_types` secara `ORDER BY id ASC`, seluruh transaksi mengakuisisi lock dalam graf asiklik yang sama. Selain itu, operasi `Cancel`, `Expire`, dan `LateWebhook` selalu mengunci `orders (id ASC)` sebelum `ticket_types (id ASC)`.
- **Trade-Off:** Throughput per satu baris event ter-serialisasi oleh PostgreSQL row lock, bukan eksekusi paralel penuh seperti sharded counter. Untuk satu event berkuota ratusan hingga ribuan tiket, serialisasi per baris di PostgreSQL menyelesaikan ratusan transaksi dalam puluhan milidetik dengan jaminan konsistensi ketat.

---

## ADR-004: Advisory Lock Per Pasangan `(event_id, buyer_id)` untuk Limit 4 Tiket

- **Konteks:** Aturan bisnis 4 membatasi 1 pembeli maksimal memiliki 4 tiket per event lintas seluruh pesanan `WAITING_PAYMENT` dan `PAID`. Pembeli yang mengirim 2 request HTTP paralel masing-masing berisi 3 tiket akan lolos jika hanya mengandalkan `SELECT SUM(total_tickets)`.
- **Keputusan:** Pada awal transaksi `CreateOrder`, jalankan `SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))` dengan parameter `(event_id::text, buyer_id::text)`.
- **Alternatif yang Ditolak:** Transaction isolation level `SERIALIZABLE`. Pada beban 200 concurrent buyers ke event yang sama, `SERIALIZABLE` dapat memicu serialization failure (`40001`) antar pembeli yang berbeda sehingga menyulitkan klien yang sah ketika retry tidak diterapkan.
- **Alasan & Trade-Off:** `pg_advisory_xact_lock(event_id, buyer_id)` **hanya memblokir pembeli yang sama di event yang sama**. Buyer A dan Buyer B tetap berjalan paralel penuh, sedangkan dua request paralel dari Buyer A mengantre otomatis lalu yang kedua langsung ditolak dengan `409 BUYER_LIMIT_EXCEEDED`. Ada peluang sangat kecil *hash collision* 64-bit (`2^-64`), yang dampaknya pun hanya serialisasi sesaat, tidak pernah merusak data.

---

## ADR-005: Hybrid Expiry Strategy (Ticker `SKIP LOCKED` + Partial-Index Lazy Check)

- **Konteks:** Pesanan yang tidak dibayar melewati 15 menit harus otomatis menjadi `EXPIRED` dan mengembalikan kuota, sementara `GET /events` harus menampilkan sisa kuota *real-time*.
- **Keputusan:**
  1. **Background Ticker (setiap 5 detik):** Mengambil batch hingga 50 pesanan lewat batas waktu dengan `SELECT id FROM orders WHERE status = 'WAITING_PAYMENT' AND expires_at <= NOW() ORDER BY id ASC FOR UPDATE SKIP LOCKED LIMIT 50`, memindahkannya ke `EXPIRED`, mengembalikan `remaining_quota`, dan mencatat `order_logs` dengan `actor_type = 'scheduler'`.
  2. **Lazy Check dengan Partial Index:** Membuat partial index `CREATE INDEX idx_orders_active_expiry ON orders (event_id, expires_at) WHERE status = 'WAITING_PAYMENT'`. Setiap `GET /events`, `GET /events/{id}`, `POST /orders`, `Cancel`, `Pay`, dan `Webhook` mengecek due rows terlebih dahulu.
- **Mengapa Tidak Menambah Redis / Queue Worker Terpisah?**
  Sesuai batasan proyek (`final-project.md`: tanpa microservice berlebihan dan 1 perintah `docker compose up`), menambah container Redis + broker job untuk 1 jadwal expiry 15 menit adalah *over-engineering* dan menambah titik sinkronisasi state (bila Redis tidak sinkron dengan PostgreSQL).
- **Trade-Off:** Pada cluster multi-instance, dua node API bisa memindai tabel yang sama; klausa PostgreSQL `FOR UPDATE SKIP LOCKED` memastikan dua worker tidak pernah memproses order yang sama atau mengembalikan kuota dua kali. Di sisi lain, saat beban normal tanpa order kedaluwarsa, partial index menjaga biaya pre-check tetap minimal (`0` heap lock).

---

## ADR-006: Idempotency Webhook & Kebijakan Late Payment ("Quota Safety First" vs "PAID Always Wins")

- **Konteks:** Webhook pembayaran bisa terkirim ganda secara paralel, memiliki nominal tidak cocok, memiliki signature tidak valid, atau datang **setelah** pesanan melewati batas 15 menit (`EXPIRED`).
- **Alternatif Kebijakan ("PAID Always Wins") vs "Quota Safety First":**
  Pada sistem tanpa kuota terbatas (misalnya booking jasa non-kapasitas), webhook `payment.capture` yang terlambat datang setelah tagihan `EXPIRED` kadang tetap dipromosikan menjadi `PAID`.
- **Keputusan di Treean ("Quota Safety First"):**
  Untuk penjualan tiket berkuota terbatas, begitu order melewati 15 menit (`EXPIRED`), kuotanya sudah kembali ke pool dan mungkin sudah dibeli pengguna lain. Memaksa promosi ke `PAID` setelah `EXPIRED` akan menyebabkan **oversell**. Oleh karena itu:
  1. **Validasi Keamanan Fail-Closed:** Header/field signature `SHA-512(order_id + status_code + gross_amount + secret)` wajib diverifikasi dalam waktu konstan (`hmac.Equal`). Jika secret konfigurasi kosong, semua webhook ditolak (`401 UNAUTHORIZED_WEBHOOK`).
  2. **Audit Log Di Luar Transaksi Utama:** Payload webhook langsung dicatat ke tabel `payment_webhooks` di luar transaksi rollback bisnis. Jadi meskipun signature gagal, `gross_amount` salah (`400 PAYMENT_AMOUNT_MISMATCH`), atau webhook ditolak karena `EXPIRED`, bukti auditnya tetap tersimpan.
  3. **Idempotency Replay:** Ketika order sudah `PAID`, webhook capture yang masuk ulang (termasuk 10 request bersamaan) akan mengantre pada `SELECT ... FOR UPDATE` di baris `orders`, melihat status `PAID`, tidak menerbitkan tiket lagi (`UNIQUE(order_item_id, seat_sequence)` menjadi lapisan pengaman kedua), mencatat outcome `IGNORED_DUPLICATE`, dan menjawab `200 OK` beserta daftar tiket yang sudah ada.
  4. **Late Capture Setelah Expired:** Jika `status == EXPIRED`, `CANCELLED`, atau `now > expires_at`, transaksi memastikan status tercatat `EXPIRED` (dan kuota kembali jika baru terdeteksi terlambat), menolak penerbitan tiket dengan `409 ORDER_EXPIRED`, dan menandai baris `payment_webhooks` dengan `outcome = REJECTED_LATE_EXPIRED` serta `requires_refund = true`.
- **Trade-Off:** Penyelenggara atau tim keuangan perlu menindaklanjuti flag `requires_refund = true` jika uang pengguna terlanjur terpotong oleh penyedia pembayaran pada detik ke-901, alih-alih menerbitkan tiket yang melampaui kapasitas venue.

---

## ADR-007: Penomoran Invoice Memakai PostgreSQL `SEQUENCE` dan Kode Tiket `crypto/rand`

- **Konteks:** Penomoran invoice memakai query `ORDER BY id DESC` dengan `FOR UPDATE` dapat menjadi bottleneck global di bawah tekanan checkout paralel.
- **Keputusan:** Mengganti pola `ORDER BY DESC FOR UPDATE` dengan PostgreSQL sequence (`nextval('invoice_seq')`) untuk `invoice_number` (`INV-YYYY-000001`), dan menggunakan `crypto/rand` hex/alphanumeric token untuk `tickets.ticket_code` (`TKT-XXXXXXXX`).
- **Alasan & Trade-Off:** Pada 200 concurrent checkout, mengunci baris order terbaru via `FOR UPDATE` memaksa semua 200 transaksi saling tunggu meskipun memesan event yang berbeda. Sequence bekerja di luar MVCC lock transaksi, dan kode tiket acak `crypto/rand` mencegah penebakan kode tiket milik pembeli lain. Konsekuensinya, jika sebuah transaksi `ROLLBACK`, nomor urut `invoice_seq` bisa meloncat (ada celah angka), yang sepenuhnya normal dalam sistem akuntansi modern dan tidak memengaruhi keunikan invoice.

---

## ADR-008: Hybrid Auth (JWT + Opt-in Demo Header)

- **Konteks:** Penilaian menyebutkan auth tidak menjadi fokus utama nilai, tetapi sistem harus membedakan role `ORGANIZER` vs `BUYER`, serta mendukung uji 200 pembeli bersamaan dan demo 5 menit secara lancar.
- **Keputusan:** Menyediakan endpoint standar `POST /api/v1/auth/register` dan `POST /api/v1/auth/login` yang mengeluarkan JWT Bearer, plus dukungan opsional header `X-User-ID` dan `X-User-Role` yang hanya aktif bila environment variable `ALLOW_DEMO_HEADER_AUTH=true`.
- **Alasan & Trade-Off:** Production build secara default dapat menonaktifkan header demo dengan `ALLOW_DEMO_HEADER_AUTH=false`, sementara saat `docker compose up` lokal untuk demo dan pengujian 200 concurrent buyers, pengujian dapat berjalan cepat dengan 200 identitas UUID yang sah (atau JWT penuh di test suite) tanpa bottleneck pembuatan 200 token bcrypt terlebih dahulu.

---

## ADR-009: Single-Entry-Point Nginx Edge, Redis `fail-open` Rate Limit & Cache, dan Checkout `Idempotency-Key`

- **Konteks:** Pada flash sale, pembeli sering mengirim klik ganda/retry jaringan saat checkout, bot dapat membanjiri API atau mendaftar banyak akun dari satu IP (*Sybil attack*), dan ribuan pembaca katalog event memicu query PostgreSQL berulang kali. Di sisi infra, hanya gateway Nginx yang sebaiknya terekspos ke publik.
- **Keputusan:**
  1. **Single-Entry-Point Edge (`frontend/nginx.conf` + `docker-compose.yml`):** Hanya container `frontend` (Nginx) yang membuka port host (`5173`), sedangkan `api` (1 replika), `postgres`, dan `redis` terisolasi di jaringan bridge `app-network` (file `docker-compose.db-debug.yml` disediakan untuk debugging lokal/DataGrip). Nginx menimpa `X-Forwarded-For` dan `X-Real-IP` dengan `$remote_addr` agar header proxy tidak bisa dipalsukan oleh klien.
  2. **Defense-in-Depth Rate Limit (`fail-open` Redis + Nginx):** Nginx menahan flood kasar (`50r/s`, `burst=250 nodelay`, HTTP `429`), sementara middleware Go + Redis (`go-redis/v9` Lua script, timeout `500ms`) menerapkan aturan presisi: batas ketat per IP pada `POST /api/v1/auth/register` (`5/15m`), per `IP+hash(email)` pada `login` (`10/1m`), dan per `actor.ID` (`user_id`) pada `POST /api/v1/orders` (`20/1m`) agar pengguna di WiFi/NAT yang sama tidak saling memblokir. Bila `REDIS_URL` kosong atau Redis mati, limiter otomatis **fail-open**.
  3. **Short-TTL Event Catalog Cache (`EVENT_CACHE_TTL=3s`):** `GET /api/v1/events` dan `GET /api/v1/events/{id}` memakai read-through Redis cache yang otomatis di-invalidate pada mutasi event, order baru, cancel, webhook gagal/kadaluarsa, dan `ExpirySweeper`. Jalur checkout dan console organizer tetap membaca PostgreSQL secara langsung.
  4. **PostgreSQL Checkout `Idempotency-Key`:** Partial unique index `(buyer_id, idempotency_key)` di tabel `orders` (`003_order_idempotency.sql`) memastikan retry/klik ganda dengan kunci yang sama mengembalikan order pertama (`201 Created` + header `Idempotent-Replay: true`) tanpa mengurangi kuota kedua kali.
- **Trade-Off:** Menambah satu service `redis:7-alpine` di Compose dan ketergantungan `go-redis/v9`, tetapi seluruh jaminan kebenaran kuota dan idempotensi transaksi tetap bertahan penuh di PostgreSQL meski Redis dimatikan.
