# Catatan Keputusan Arsitektur (DECISIONS.md)

Dokumen ini menjelaskan keputusan desain utama di proyek **Treean (`github.com/Torikh42/treean`)** dengan bahasa sederhana: masalah apa yang dihadapi, solusi apa yang dipilih, alasan pemilihannya, dan apa pengorbanannya (*trade-off*).

---

## Ringkasan 1 Menit

- **Anti-Oversell:** Setiap checkout mengunci baris tipe tiket (`ticket_types`) secara berurutan dari ID kecil ke besar (`ORDER BY id ASC FOR UPDATE`) dan dijaga batas database `remaining_quota >= 0`.
- **Batas 4 Tiket per Pembeli:** Pembelian paralel dari pembeli yang sama di satu acara diantrekan sebentar memakai `pg_advisory_xact_lock`, tanpa memblokir pembeli lain.
- **Hold 15 Menit & Auto-Expire:** Order yang belum dibayar otomatis dibatalkan oleh pekerja latar belakang (*background ticker* tiap `5 detik` memakai `FOR UPDATE SKIP LOCKED`) sekaligus dibersihkan langsung saat data dibaca atau dipesan (*lazy check*).
- **Webhook Aman & Anti-Dobel:** Notifikasi pembayaran wajib pakai tanda tangan `SHA-512`, selalu dicatat ke tabel audit `payment_webhooks` meski gagal, dan memakai prinsip **Quota Safety First** (jika pembayaran telat datang setelah order kadaluarsa, tiket ditolak dan ditandai butuh refund).
- **Single Entry Point + Redis Fail-Open + Idempotency-Key:** Hanya port `:5173` (Nginx) yang terbuka ke luar. Redis menangani rate limit dan cache katalog `3 detik` secara *fail-open* (kalau Redis mati, aplikasi tetap jalan), sementara header `Idempotency-Key` mencegah pesanan ganda akibat klik dua kali.

---

## Daftar Keputusan

| ID | Topik | Pilihan Utama | Pilihan yang Tidak Dipakai |
| :--- | :--- | :--- | :--- |
| `ADR-001` | Bahasa & Stack Backend | Go 1.26 (`go1.26.2`) + `chi/v5` + `Huma v2` + `pgx/v5` | Framework Node/Bun, atau ORM berat (GORM/ent) |
| `ADR-002` | Struktur Kode | Arsitektur Berlapis (`domain`, `handler`, `service`, `repository`, dll.) | MVC tradisional, atau Microservices terpisah |
| `ADR-003` | Anti-Oversell & Anti-Deadlock | Kunci baris `ticket_types` terurut `ASC` (`FOR UPDATE`) + `CHECK` constraint | Optimistic Locking (`version` retry loop) |
| `ADR-004` | Batas Maks 4 Tiket/Pembeli/Acara | `pg_advisory_xact_lock(hashtext(event), hashtext(buyer))` | `SELECT SUM()` polos, atau level isolasi `SERIALIZABLE` |
| `ADR-005` | Auto-Expire 15 Menit | Gabungan Ticker `5s` (`FOR UPDATE SKIP LOCKED`) + Partial-Index Lazy Check | Ticker saja, atau antrean delay di Redis |
| `ADR-006` | Webhook & Pembayaran Telat | Audit log di luar rollback + prinsip **Quota Safety First** | Pola "PAID Always Wins", atau cache idempotency di RAM |
| `ADR-007` | Nomor Invoice & Kode Tiket | PostgreSQL `SEQUENCE` (`invoice_seq`) + acak aman `crypto/rand` | Query `SELECT MAX(...) FOR UPDATE` pada tabel `orders` |
| `ADR-008` | Autentikasi Demo & Load Test | Hybrid JWT Bearer + Demo Header opsional (`ALLOW_DEMO_HEADER_AUTH`) | Wajib token JWT `bcrypt` untuk simulasi 200 pembeli |
| `ADR-009` | Nginx Edge, Redis Fail-Open & Checkout Idempotency | Nginx satu pintu (`:5173`), Redis *fail-open* (rate limit + cache `3s`), dan DB `Idempotency-Key` | Ekspos semua port ke host, rate limit IP saja, tanpa key checkout |

---

## ADR-001: Go 1.26 + `chi/v5` + `Huma v2` + `pgx/v5` Tanpa ORM

- **Masalah:** Sistem flash sale harus sanggup menahan `200` permintaan paralel tanpa balapan kuota, melakukan penguncian SQL yang presisi, serta punya dokumentasi API interaktif untuk live demo 5 menit.
- **Keputusan:** Memakai Go `1.26.2` dengan driver SQL murni `pgx/v5` (`pgxpool`), router `go-chi/chi/v5`, dan `Huma v2` sebagai generator OpenAPI 3.1 + Scalar UI (`/docs`).
- **Kenapa & Pengorbanan:**
  - Dengan SQL langsung lewat `pgx/v5`, kita tahu persis kapan dan di urutan mana baris database dikunci (`ORDER BY id ASC FOR UPDATE`, `SKIP LOCKED`, `pg_advisory_xact_lock`), tanpa query tersembunyi dari ORM.
  - `Huma v2` otomatis memvalidasi input dari struct tag Go (misal jumlah tiket `1..4`, format UUID) sekaligus menghasilkan `/openapi.json` dan halaman `/docs` yang langsung sinkron dengan SDK TypeScript frontend.
  - **Pengorbanan:** Kita menulis query SQL dan mapping baris (`pgx.CollectRows`) secara manual, jadi kode repository sedikit lebih panjang dibanding ORM, namun jauh lebih aman dari query N+1 atau deadlock tak terduga.

---

## ADR-002: Struktur Berlapis yang Rapi (`internal/domain` Bersih)

- **Masalah:** Kode harus mudah dirawat, mudah diuji unit test tanpa menyalakan database, tetapi tidak boleh terlalu rumit seperti memecah proyek kecil menjadi puluhan microservice.
- **Keputusan:** Memisahkan kode ke layer horizontal (`handler`, `service`, `repository`, `middleware`, `redisstore`, `routes`, `worker`) dengan `internal/domain` sebagai pusat entitas bisnis. Package `service` mendefinisikan interface kecil untuk kebutuhannya sendiri (pola *consumer-defined interface*).
- **Kenapa & Pengorbanan:**
  - Package `internal/domain` hanya memakai library standar Go dan konstanta kode status dari `net/http` (tidak ada import database driver, SQL, atau router HTTP). Aturan status order (`CanPay()`, `CanCancel()`) dan batas `MaxTicketsPerBuyerPerEvent = 4` bisa dites sebagai pure unit test dalam milidetik.
  - **Pengorbanan:** Tetap ada sedikit proses konversi atau interface antar layer, tetapi jauh lebih ringan daripada full microservices ketika transaksi harus mengunci `orders`, `order_items`, dan `ticket_types` sekaligus.

---

## ADR-003: Kunci Baris Terurut (`ORDER BY id ASC FOR UPDATE`) untuk Anti-Oversell

- **Masalah:** Ada dua risiko utama saat flash sale:
  1. **Oversell:** `200` pembeli memesan `100` tiket di milidetik yang sama.
  2. **Deadlock AB-BA:** Pembeli boleh membeli lebih dari 1 tipe tiket dalam satu order. Kalau Pembeli A mengunci `[VIP -> Reguler]` sedangkan Pembeli B mengunci `[Reguler -> VIP]` bersamaan, keduanya bisa saling mengunci selamanya (*deadlock* `40P01`).
- **Keputusan:** Sebelum mengurangi `remaining_quota`, transaksi mengunci seluruh baris `ticket_types` yang diminta secara berurutan dari UUID terkecil:
  `SELECT ... FROM ticket_types WHERE id = ANY($1) AND event_id = $2 ORDER BY id ASC FOR UPDATE`, lalu diperkuat oleh batas database `CHECK (remaining_quota >= 0 AND remaining_quota <= total_quota)`. Pada aksi pembatalan, kadaluarsa, atau webhook, baris `orders` dikunci terlebih dahulu sebelum mengunci `ticket_types` secara terurut `ASC`.
- **Kenapa Tidak Optimistic Locking (`version` column)?**
  Pada flash sale dengan `200` request paralel ke baris yang sama, `199` request lain akan gagal bentrok `version` meskipun kuota sebenarnya masih tersisa `99`. Client harus mengulang (*retry storm*) yang makin membebani server. Dengan `FOR UPDATE`, PostgreSQL mengantrekan transaksi secara rapi: tepat `100` pembeli berhasil dan `100` sisanya langsung mendapat `409 QUOTA_EXHAUSTED`.
- **Pengorbanan:** Pemesanan ke event yang sama dieksekusi bergantian di level baris tiket, bukan paralel penuh. Untuk ribuan transaksi, antrean PostgreSQL ini selesai dalam puluhan milidetik dengan kepastian stok 100% akurat.

---

## ADR-004: Kunci Sementara Per `(event_id, buyer_id)` untuk Batas 4 Tiket

- **Masalah:** Aturan bisnis membatasi 1 pembeli maksimal memegang `4` tiket per acara (gabungan order `WAITING_PAYMENT` dan `PAID`). Jika seorang pembeli mengirim 2 request bersamaan masing-masing `3` tiket, pengecekan biasa (`SELECT SUM(...)`) bisa sama-sama melihat total awal `0` lalu meloloskan total `6` tiket.
- **Keputusan:** Di awal transaksi checkout, jalankan `SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))` dengan parameter `(event_id, buyer_id)` (lihat [`internal/repository/order_repo.go`](internal/repository/order_repo.go)). Kunci ini otomatis dilepas oleh PostgreSQL begitu transaksi commit atau rollback.
- **Kenapa Tidak Level Isolasi `SERIALIZABLE`?**
  Dengan `SERIALIZABLE`, `200` pembeli berbeda yang memesan di acara yang sama bisa saling memicu error `40001 serialization failure` sehingga request pembeli yang sah ikut gagal kalau tidak retry.
- **Kenapa & Pengorbanan:**
  - `pg_advisory_xact_lock` **hanya mengantrekan request dari pembeli yang sama di acara yang sama**. Pembeli A dan Pembeli B tetap berjalan paralel penuh.
  - PostgreSQL `hashtext()` menghasilkan integer 32-bit untuk tiap argumen, sehingga kombinasi dua parameter membentuk ruang kunci 64-bit. Peluang dua pasangan berbeda punya hash persis sama hanya `2^-64`, dan dampaknya pun sekadar antre sesaat, tidak pernah merusak data kuota.

---

## ADR-005: Gabungan Ticker `5 Detik` (`SKIP LOCKED`) & Pengecekan Langsung (*Lazy Check*)

- **Masalah:** Pesanan yang tidak dibayar lewat dari `15 menit` harus otomatis berubah menjadi `EXPIRED`, mengembalikan kuota ke pool, dan endpoint `GET /api/v1/events` harus menampilkan stok terbaru saat itu juga.
- **Keputusan:** Menggabungkan dua mekanisme berbasis jam database PostgreSQL (`NOW()`):
  1. **Worker Periodik (`worker.ExpirySweeper`, tiap `5 detik`):** Mengambil maksimal `50` order kadaluarsa dengan `ORDER BY id ASC FOR UPDATE SKIP LOCKED LIMIT 50`, mengembalikan `remaining_quota`, mengubah status menjadi `EXPIRED`, mencatat audit ke `order_logs`, dan menghapus cache Redis event yang terdampak.
  2. **Lazy Check dengan Partial Index:** Tabel `orders` punya partial index `idx_orders_active_expiry` dan `idx_orders_global_expiry` (`WHERE status = 'WAITING_PAYMENT'`). Sebelum membaca stok acara atau membuat order baru, sistem memeriksa dulu apakah ada order lewat waktu dan langsung melepaskannya saat itu juga.
- **Posisi PostgreSQL vs Redis:** Penjadwalan expiry `15 menit` sengaja tetap dipegang penuh oleh PostgreSQL (bukan antrean delay di Redis) agar tidak pernah terjadi selisih data antara state timer Redis dan stok database. Redis yang ditambahkan belakangan di `ADR-009` murni berperan sebagai layer pembantu (*rate limiter* dan cache `3 detik`), dan `ExpirySweeper` bertugas membersihkan cache tersebut setiap selesai menyapu order kadaluarsa.
- **Pengorbanan:** Jika suatu saat API dijalankan banyak replika, klausa `FOR UPDATE SKIP LOCKED` memastikan dua worker tidak pernah mengambil dan me-restock order yang sama dua kali. Sebaliknya, saat tidak ada order kadaluarsa, query pengecekan partial index sangat ringan dan tidak mengunci baris apa pun.

---

## ADR-006: Audit Webhook Terpisah & Prinsip "Quota Safety First"

- **Masalah:** Payment gateway bisa mengirim webhook yang sama berulang kali secara paralel, mengirim nominal yang salah, mengirim tanda tangan palsu, atau mengirim notifikasi **terlambat** setelah batas waktu `15 menit` lewat dan kuota tiket keburu dibeli orang lain.
- **Keputusan:**
  1. **Verifikasi Tanda Tangan Ketat (*Fail-Closed*):** Signature `SHA-512(order_id + status_code + gross_amount + WEBHOOK_SECRET)` dibandingkan dengan `hmac.Equal` (waktu konstan). Jika `WEBHOOK_SECRET` kosong, semua webhook langsung ditolak dengan `401 UNAUTHORIZED_WEBHOOK`.
  2. **Audit Log Di Luar Transaksi Bisnis:** Setiap panggilan webhook langsung disimpan ke tabel `payment_webhooks` melalui koneksi pool di luar transaksi utama (`WebhookRepo.LogDelivery`). Jadi meskipun transaksi bisnis ditolak atau di-rollback karena tanda tangan salah (`SIGNATURE_INVALID`), nominal beda (`400 PAYMENT_AMOUNT_MISMATCH`), atau telat bayar (`REJECTED_LATE_EXPIRED`), bukti audit tetap tersimpan.
  3. **Webhook Ganda Tetap Idempoten:** Jika order sudah `PAID`, webhook ulangan (termasuk `10` request bersamaan) mengantre di `SELECT ... FOR UPDATE`, melihat status `PAID`, tidak menerbitkan tiket baru (diperkuat constraint database `UNIQUE (order_item_id, seat_sequence)`), mencatat `IGNORED_DUPLICATE`, lalu menjawab `200 OK` (`idempotent_replay: true`) dengan daftar tiket yang sudah ada.
  4. **Terlambat Bayar = Quota Safety First:** Jika order sudah `EXPIRED`, `CANCELLED`, atau melewati `expires_at`, sistem menolak penerbitan tiket dengan `409 ORDER_EXPIRED` (atau `409 INVALID_ORDER_STATE` jika dibatalkan), memastikan kuota kembali jika belum terlepas, dan menandai `outcome = REJECTED_LATE_EXPIRED` serta `requires_refund = true`.
- **Pengorbanan:** Daripada memakai aturan "PAID Always Wins" yang memaksa tiket terbit dan bikin kapasitas gedung jebol (*oversell*), sistem memilih menolak tiket yang telat bayar dan mengandalkan kolom `requires_refund = true` untuk ditindaklanjuti ke alur pengembalian dana.

---

## ADR-007: Nomor Invoice Pakai `SEQUENCE` & Kode Tiket Pakai `crypto/rand`

- **Masalah:** Jika nomor urut invoice dibuat dengan mengunci baris terakhir (`SELECT ... ORDER BY id DESC FOR UPDATE`), seluruh `200` pembeli paralel terpaksa saling antre hanya untuk menghitung nomor urut invoice meskipun memesan event yang berbeda.
- **Keputusan:** Memakai PostgreSQL sequence (`SELECT nextval('invoice_seq')`) untuk format `INV-YYYY-000001`, dan `crypto/rand` untuk membuat kode tiket unik `TKT-YYMMDD-XXXXXXXXXX` (`5` byte acak = `10` karakter hex huruf besar, dicek di [`internal/repository/order_repo.go`](internal/repository/order_repo.go)).
- **Kenapa & Pengorbanan:**
  - `nextval('invoice_seq')` berjalan bebas tanpa menahan kunci tabel antar pembeli, sedangkan `crypto/rand` mencegah orang menebak kode tiket milik pembeli lain secara berurutan.
  - **Pengorbanan:** Jika ada transaksi checkout yang gagal/rollback setelah mengambil `nextval`, nomor invoice bisa melompat satu angka (misalnya `000010` lalu `000012`). Hal ini wajar dan tetap menjamin setiap invoice unik.

---

## ADR-008: Auth Hybrid (JWT Bearer + Demo Header Opsional)

- **Masalah:** Aplikasi web membutuhkan autentikasi standar dan pembagian peran (`ORGANIZER` vs `BUYER`), namun di sisi lain pengujian `200` pembeli paralel harus bisa berjalan cepat tanpa perlu mendaftarkan `200` akun dengan hash `bcrypt` terlebih dahulu.
- **Keputusan:** Endpoint `/api/v1/auth/register` dan `/api/v1/auth/login` mengeluarkan token JWT HS256 standar, ditambah mekanisme header demo `X-User-ID` dan `X-User-Role` yang hanya aktif jika `ALLOW_DEMO_HEADER_AUTH=true` (lihat [`internal/middleware/auth.go`](internal/middleware/auth.go)).
- **Kenapa & Pengorbanan:**
  - Nilai default di kode (`config.Load()`) dan `.env.example` adalah `ALLOW_DEMO_HEADER_AUTH=false` agar aman untuk production, sedangkan di `docker-compose.yml` diaktifkan (`true`) agar demo lokal dan load test cepat.
  - Web console tetap login memakai Bearer JWT murni, sedangkan halaman `/flash-sale` memanggil simulator Go di sisi backend.

---

## ADR-009: Gateway Nginx (`:5173`), Redis *Fail-Open*, dan Checkout `Idempotency-Key`

- **Masalah:**
  1. Port backend, database, dan Redis tidak seharusnya terbuka langsung ke laptop/publik.
  2. Pembeli di WiFi kampus atau kantor berbagi satu IP publik yang sama, jadi rate limit berbasis IP murni berisiko memblokir pembeli asli saat ramai.
  3. Saat flash sale, banyak request `GET /api/v1/events` berulang kali menekan PostgreSQL, dan klik dua kali saat koneksi lambat bisa membuat dua pesanan kembar.
- **Keputusan:**
  1. **Single Entry Point Nginx (`:5173`):** Hanya container `frontend` (Nginx) yang membuka port host `5173`. `api` (`8080`), `postgres` (`5432`), dan `redis` (`6379`) terisolasi di jaringan internal `app-network` (file `docker-compose.db-debug.yml` disediakan khusus bila ingin membuka port saat debugging lokal). Nginx memfilter banjir request kasar (`50 req/s`, `burst=250`), mengaktifkan gzip dan security headers, serta menimpa `X-Forwarded-For` dengan `$remote_addr` agar klien tidak bisa memalsukan IP.
  2. **Redis *Fail-Open* (Rate Limit Presisi + Cache `3 Detik`):** Package `internal/redisstore` menerapkan batas waktu ketat `REDIS_TIMEOUT=500ms`. Jika Redis mati atau lambat, sistem otomatis **fail-open** (tetap melayani pembeli lewat PostgreSQL):
     - **Rate Limiting Atomic Lua Script (`INCR` + `PEXPIRE` + `PTTL`):** Dijalankan setelah auth middleware supaya checkout (`POST /api/v1/orders`) dan batal dibatasi per `user_id` (`20/menit`) dengan batas IP longgar (`300/menit`). Registrasi dibatasi ketat per IP (`5/15 menit`), login per `IP + email` (`10/menit`), dan demo flash sale per IP (`5/menit`).
     - **Event Catalog Cache (`EVENT_CACHE_TTL=3s`):** Menyimpan hasil `GET /api/v1/events` dan `GET /api/v1/events/{id}` di Redis selama `3 detik`, dan otomatis dihapus (*invalidated*) setiap ada perubahan event, order baru, pembatalan, webhook pembayaran, atau sweep order kadaluarsa. Halaman organizer dan checkout tetap membaca PostgreSQL langsung.
  3. **Checkout `Idempotency-Key` di PostgreSQL:** Migrasi `003_order_idempotency.sql` menambah kolom `idempotency_key` (maks `128` karakter) dan partial unique index `uidx_orders_buyer_idempotency_key` pada `(buyer_id, idempotency_key)`. Jika pembeli mengulang checkout dengan `Idempotency-Key` yang sama, server mengembalikan order pertama beserta header `Idempotent-Replay: true` tanpa memotong kuota lagi.
- **Pengorbanan:**
  - Menambah container `redis:7-alpine`, tetapi kebenaran kuota dan idempotensi transaksi tetap ditopang penuh oleh PostgreSQL meski Redis dimatikan.
  - **Trade-off `Idempotency-Key`:** Saat ini deduplikasi dicek langsung dari pasangan `(buyer_id, idempotency_key)` tanpa menyimpan hash isi keranjang. Artinya jika key yang sama dikirim dua kali, server langsung me-replay order awal (`201 Created`). Ini aman dipakai karena frontend ([`frontend/src/hooks/useEventCheckout.ts`](frontend/src/hooks/useEventCheckout.ts)) selalu meng-generate UUID baru setiap sesi checkout dan me-reset key setiap kali jumlah tiket diubah. Jika kelak API dibuka ke partner B2B eksternal, pengecekan payload hash dapat ditambahkan dengan menambah kolom hash di migrasi baru.

---

## Tanya-Jawab Cepat Saat Demo (Bekal Demo 5 Menit)

- **"Bagaimana cara mencegah oversell saat 200 orang pesan 100 tiket bersamaan?"**
  Setiap checkout mengunci baris `ticket_types` dengan `SELECT ... FOR UPDATE` terurut berdasarkan `id ASC` dan dijaga constraint database `remaining_quota >= 0`. PostgreSQL mengantrekan transaksi secara deterministik: `100` sukses dan `100` ditolak dengan `409 QUOTA_EXHAUSTED`.
- **"Bagaimana cara menjaga batas maksimal 4 tiket per pembeli kalau request dikirim paralel?"**
  Memakai `pg_advisory_xact_lock(hashtext(event_id), hashtext(buyer_id))` di awal transaksi. Hanya request milik pembeli dan acara yang sama yang antre; pembeli lain tetap berjalan paralel penuh.
- **"Bagaimana kalau worker pembersih order kadaluarsa 15 menit lagi mati atau terlambat?"**
  Sebelum membaca stok acara atau checkout baru, API menjalankan *lazy check* ke partial index `WAITING_PAYMENT` sehingga order yang sudah melewati `expires_at` langsung dilepas saat itu juga.
- **"Bagaimana kalau payment gateway mengirim webhook dua kali secara bersamaan?"**
  Row lock `SELECT ... FOR UPDATE` pada baris `orders` membuat request kedua menunggu request pertama selesai menjadi `PAID`, lalu membalas `200 OK` dengan `idempotent_replay: true` dan daftar tiket yang sama tanpa mencetak tiket ganda.
- **"Bagaimana kalau ada pembeli yang bayar telat di menit ke-16 saat kuota sudah dibeli orang lain?"**
  Sistem memegang prinsip **Quota Safety First**: webhook ditolak dengan `409 ORDER_EXPIRED`, tiket tidak diterbitkan agar kapasitas tidak jebol, dan status log ditandai `requires_refund = true`.
- **"Bagaimana kalau Redis tiba-tiba mati saat hari H flash sale?"**
  Semua fitur Redis (rate limit dan cache `3 detik`) bersifat **fail-open** dengan timeout `500ms`. Jika Redis mati, traffic langsung lanjut ke PostgreSQL tanpa membuat pembeli gagal checkout, dan integritas kuota tetap aman karena seluruh lock ada di PostgreSQL.
- **"Kenapa semua akses masuk lewat port `:5173` saja?"**
  Nginx bertindak sebagai *Single Entry Point* (menyajikan frontend React sekaligus mem-proxy `/api` dan `/docs` ke `api:8080`). Port PostgreSQL (`5432`), Redis (`6379`), dan API (`8080`) diisolasi di dalam jaringan internal Docker agar lebih aman dari akses luar.
