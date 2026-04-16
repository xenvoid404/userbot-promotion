# userbot-promotion

> Telegram Userbot untuk otomatisasi pengiriman pesan promosi — dibangun dengan Go, SQLite, dan REST API.

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-green)](LICENSE)
[![SQLite](https://img.shields.io/badge/Database-SQLite-003B57?logo=sqlite)](https://sqlite.org)

---

## Daftar Isi

- [Fitur](#fitur)
- [Arsitektur](#arsitektur)
- [Prasyarat](#prasyarat)
- [Instalasi](#instalasi)
- [Konfigurasi Awal](#konfigurasi-awal)
- [REST API Reference](#rest-api-reference)
- [Deploy dengan systemd](#deploy-dengan-systemd)
- [Cara Kerja Scheduler](#cara-kerja-scheduler)
- [Struktur Project](#struktur-project)
- [Catatan Keamanan](#catatan-keamanan)

---

## Fitur

| Fitur | Keterangan |
|---|---|
| ✅ Whitelist grup | Wajib whitelist dulu sebelum bot bisa mengirim ke grup |
| 🚫 Blacklist grup | Kecualikan grup secara permanen; auto-blacklist jika bot di-kick |
| 📨 Multi-pesan promosi | Setiap pesan punya target, jadwal, dan konten sendiri |
| 🎲 Waktu kirim acak | Pengiriman tersebar merata dalam 24 jam dengan slot acak |
| 🔢 Limit per grup | Setiap grup bisa punya limit sendiri atau ikuti limit global |
| 📋 Filter pesan per grup | Tentukan pesan mana yang boleh masuk ke grup tertentu |
| 💬 Support topik supergrup | Kirim ke topik spesifik via `topic_id` |
| 🖼️ Support media | Kirim gambar/video beserta caption |
| 🌐 REST API | Kelola semua konfigurasi secara dinamis — tanpa restart |
| 📊 Log & statistik | Riwayat pengiriman dan statistik per grup/pesan |
| 🚀 Single binary | Build sekali, deploy ke mana saja — tidak ada dependency runtime |
| 🛡️ systemd ready | Unit file lengkap dengan hardening dan restart policy |

---

## Arsitektur

```
┌─────────────────────────────────────────────────────┐
│                  userbot-promotion                   │
│                                                     │
│  ┌──────────────┐    ┌──────────────────────────┐  │
│  │  REST API    │    │       Scheduler          │  │
│  │  (chi)       │    │  - Build jobs 24h        │  │
│  │              │───▶│  - Random slot per grup  │  │
│  │  Port: 8080  │    │  - Worker pool (5 gorout)│  │
│  └──────────────┘    └────────────┬─────────────┘  │
│         │                         │                 │
│  ┌──────▼─────────────────────────▼─────────────┐  │
│  │              SQLite (WAL mode)                │  │
│  │  config · messages · groups · send_log        │  │
│  └───────────────────────────────────────────────┘  │
│                         │                           │
│  ┌──────────────────────▼──────────────────────┐   │
│  │         Telegram Client (gotd/td)            │   │
│  │  - MTProto userbot (bukan Bot API)           │   │
│  │  - Auto flood-wait handling + retry          │   │
│  │  - Auto blacklist jika forbidden             │   │
│  └─────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────┘
```

**Stack:**

| Komponen | Library | Alasan |
|---|---|---|
| Telegram | [gotd/td](https://github.com/gotd/td) | MTProto userbot, aktif dikembangkan |
| HTTP Router | [chi](https://github.com/go-chi/chi) | Ringan, idiomatic, middleware composable |
| Database | SQLite + [go-sqlite3](https://github.com/mattn/go-sqlite3) | Zero dependency server, WAL mode |
| Logger | [zap](https://github.com/uber-go/zap) | Structured, zero-alloc, production-ready |
| Deploy | systemd | Single binary, restart policy, journald logging |

---

## Prasyarat

- **Go 1.22+** dengan CGO support (`gcc` diperlukan untuk `go-sqlite3`)
- **Akun Telegram** (bukan bot — ini adalah userbot)
- **API credentials** dari [my.telegram.org/apps](https://my.telegram.org/apps)

```bash
# Ubuntu/Debian
sudo apt install build-essential golang-go

# Verifikasi
go version   # harus 1.22+
gcc --version
```

---

## Instalasi

```bash
# 1. Clone repository
git clone https://github.com/xenvoid404/userbot-promotion
cd userbot-promotion

# 2. Download dependency
go mod tidy

# 3. Build binary
make build-local   # untuk development (OS saat ini)
make build         # untuk deploy (Linux amd64)

# 4. Jalankan
./bin/userbot-promotion
```

Database `sqlite.db` dibuat otomatis beserta semua tabel saat binary pertama dijalankan.

---

## Konfigurasi Awal

Semua konfigurasi disimpan di database dan dikelola via REST API.
Saat pertama kali berjalan, `api_secret` masih `changeme` — semua request diizinkan.

### Langkah 1 — Set kredensial Telegram

```bash
curl -X PUT http://localhost:8080/config \
  -H "Content-Type: application/json" \
  -d '{"key": "api_id", "value": "123456"}'

curl -X PUT http://localhost:8080/config \
  -H "Content-Type: application/json" \
  -d '{"key": "api_hash", "value": "abcdef1234567890abcdef1234567890"}'

curl -X PUT http://localhost:8080/config \
  -H "Content-Type: application/json" \
  -d '{"key": "phone_number", "value": "+628123456789"}'
```

### Langkah 2 — Restart binary untuk login

Restart binary. Saat pertama kali dengan config lengkap, bot akan meminta kode OTP
di terminal. Masukkan kode dari Telegram — sesi disimpan otomatis ke `session.json`.

```bash
./bin/userbot-promotion
# Masukkan kode OTP dari Telegram: 12345
# ✅ Login berhasil
```

### Langkah 3 — Amankan API secret

```bash
curl -X PUT http://localhost:8080/config \
  -H "Content-Type: application/json" \
  -d '{"key": "api_secret", "value": "ganti-dengan-secret-kuat-kamu"}'
```

Setelah ini semua request wajib menyertakan header:
```
Authorization: Bearer ganti-dengan-secret-kuat-kamu
```

### Langkah 4 — Tambah grup dan pesan

```bash
# Whitelist grup
curl -X POST http://localhost:8080/groups \
  -H "Authorization: Bearer ganti-dengan-secret-kuat-kamu" \
  -H "Content-Type: application/json" \
  -d '{"group_id": -1001234567890, "label": "Komunitas Dev", "limit_24h": 3}'

# Buat pesan promosi
curl -X POST http://localhost:8080/messages \
  -H "Authorization: Bearer ganti-dengan-secret-kuat-kamu" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Promo Produk A",
    "text": "🔥 *Promo spesial!*\n\nDapatkan diskon 30% hari ini.",
    "delay_between_groups": 8,
    "use_global_whitelist": true,
    "active": true
  }'
```

Bot akan mulai mengirim secara otomatis pada siklus berikutnya (maks 24 jam).

---

## REST API Reference

### Autentikasi

Semua endpoint (kecuali `/health`) membutuhkan header:

```
Authorization: Bearer <api_secret>
```

### Response Format

Semua response menggunakan format yang konsisten:

```json
{ "success": true, "data": { ... } }
{ "success": false, "message": "pesan error" }
```

---

### Health

```
GET /health
```

Tidak membutuhkan autentikasi. Untuk health check dan monitoring.

```json
{ "success": true, "data": { "status": "ok" } }
```

---

### Config

| Method | Endpoint | Keterangan |
|---|---|---|
| `GET` | `/config` | Lihat semua config (nilai sensitif disembunyikan) |
| `PUT` | `/config` | Ubah satu nilai config |

**Config keys yang tersedia:**

| Key | Default | Keterangan |
|---|---|---|
| `api_id` | `` | Dari my.telegram.org/apps |
| `api_hash` | `` | Dari my.telegram.org/apps (tersembunyi di GET) |
| `phone_number` | `` | Nomor HP format `+62...` |
| `session_string` | `` | Dikelola otomatis — tidak bisa diubah via API |
| `global_limit_24h` | `3` | Batas pengiriman default per grup per 24 jam |
| `api_port` | `8080` | Port REST API (butuh restart untuk berlaku) |
| `api_secret` | `changeme` | Bearer token untuk autentikasi API |

```bash
# Lihat semua config
curl -H "Authorization: Bearer secret" http://localhost:8080/config

# Ubah global limit
curl -X PUT -H "Authorization: Bearer secret" http://localhost:8080/config \
  -H "Content-Type: application/json" \
  -d '{"key": "global_limit_24h", "value": "5"}'
```

---

### Pesan Promosi

| Method | Endpoint | Keterangan |
|---|---|---|
| `GET` | `/messages` | List semua pesan |
| `POST` | `/messages` | Buat pesan baru |
| `GET` | `/messages/{id}` | Detail satu pesan |
| `PUT` | `/messages/{id}` | Update pesan |
| `DELETE` | `/messages/{id}` | Hapus pesan |

**Body POST/PUT:**

```json
{
  "name": "Promo Produk A",
  "text": "🔥 *Teks promosi*\n\nSupport markdown Telegram.",
  "media_path": "",
  "delay_between_groups": 8,
  "use_global_whitelist": true,
  "targets": [],
  "active": true
}
```

| Field | Tipe | Keterangan |
|---|---|---|
| `name` | string | **Wajib.** Nama unik pesan |
| `text` | string | Teks pesan (markdown Telegram) |
| `media_path` | string | Path file gambar/video di server (opsional) |
| `delay_between_groups` | int | Jeda detik antar grup dalam satu ronde (default: 5) |
| `use_global_whitelist` | bool | `true` = kirim ke semua whitelist; `false` = gunakan `targets` |
| `targets` | array int64 | Daftar group_id jika `use_global_whitelist: false` |
| `active` | bool | `false` = nonaktifkan tanpa hapus |

```bash
# Buat pesan ke target spesifik
curl -X POST -H "Authorization: Bearer secret" http://localhost:8080/messages \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Flash Sale VIP",
    "text": "⚡ Flash sale khusus member!",
    "use_global_whitelist": false,
    "targets": [-1001234567890, -1009876543210],
    "active": true
  }'

# Nonaktifkan pesan (tanpa hapus)
curl -X PUT -H "Authorization: Bearer secret" http://localhost:8080/messages/1 \
  -H "Content-Type: application/json" \
  -d '{"name": "Promo Produk A", "text": "...", "active": false}'
```

---

### Grup

| Method | Endpoint | Keterangan |
|---|---|---|
| `GET` | `/groups` | List semua grup |
| `POST` | `/groups` | Whitelist / upsert grup |
| `GET` | `/groups/{id}` | Detail satu grup |
| `PUT` | `/groups/{id}` | Update setting grup |
| `DELETE` | `/groups/{id}` | Hapus grup dari whitelist |
| `POST` | `/groups/{id}/blacklist` | Blacklist / unblacklist grup |
| `GET` | `/groups/{id}/messages` | Lihat filter pesan aktif |
| `PUT` | `/groups/{id}/messages` | Set filter pesan |

**Body POST/PUT:**

```json
{
  "group_id": -1001234567890,
  "label": "Komunitas Dev",
  "topic_id": null,
  "limit_24h": 3,
  "blacklisted": false
}
```

| Field | Tipe | Keterangan |
|---|---|---|
| `group_id` | int64 | **Wajib.** ID grup Telegram (negatif untuk supergrup) |
| `label` | string | Nama/label untuk memudahkan identifikasi |
| `topic_id` | int64 atau null | ID topik supergrup; null = chat utama |
| `limit_24h` | int atau null | Limit khusus per 24 jam; null = ikuti `global_limit_24h` |
| `blacklisted` | bool | `true` = blacklist (tidak akan menerima pesan) |

```bash
# Whitelist grup biasa
curl -X POST -H "Authorization: Bearer secret" http://localhost:8080/groups \
  -H "Content-Type: application/json" \
  -d '{"group_id": -1001234567890, "label": "Komunitas Umum"}'

# Supergrup dengan topik + limit khusus
curl -X POST -H "Authorization: Bearer secret" http://localhost:8080/groups \
  -H "Content-Type: application/json" \
  -d '{"group_id": -1001234567890, "label": "Forum Dev", "topic_id": 12345, "limit_24h": 5}'

# Blacklist grup
curl -X POST -H "Authorization: Bearer secret" \
  http://localhost:8080/groups/-1001234567890/blacklist \
  -H "Content-Type: application/json" \
  -d '{"blacklisted": true}'

# Set filter — grup ini hanya terima pesan ID 1 dan 3
curl -X PUT -H "Authorization: Bearer secret" \
  http://localhost:8080/groups/-1001234567890/messages \
  -H "Content-Type: application/json" \
  -d '{"message_ids": [1, 3]}'

# Reset filter — izinkan semua pesan
curl -X PUT -H "Authorization: Bearer secret" \
  http://localhost:8080/groups/-1001234567890/messages \
  -H "Content-Type: application/json" \
  -d '{"message_ids": []}'
```

---

### Rate Limit

| Method | Endpoint | Keterangan |
|---|---|---|
| `GET` | `/ratelimit` | Status rate limit semua grup non-blacklist |
| `DELETE` | `/ratelimit/{id}` | Reset rate limit satu grup (paksa bisa kirim lagi) |

**Response GET /ratelimit:**

```json
{
  "success": true,
  "data": [
    {
      "group_id": -1001234567890,
      "label": "Komunitas Dev",
      "limit_24h": 3,
      "sent_24h": 2,
      "remaining": 1,
      "next_available": 0,
      "window_start": 1700000000
    }
  ]
}
```

`next_available`: unix timestamp kapan slot berikutnya tersedia (0 = masih bisa kirim sekarang).

---

### Log & Statistik

| Method | Endpoint | Keterangan |
|---|---|---|
| `GET` | `/logs?limit=100` | Log pengiriman terbaru |
| `GET` | `/logs/stats?since=7d` | Statistik agregat per grup/pesan |

Parameter `since` untuk `/logs/stats`: `24h` · `7d` · `30d` · unix timestamp integer

```bash
# 50 log terakhir
curl -H "Authorization: Bearer secret" "http://localhost:8080/logs?limit=50"

# Statistik 7 hari terakhir
curl -H "Authorization: Bearer secret" "http://localhost:8080/logs/stats?since=7d"
```

---

## Deploy dengan systemd

### 1. Siapkan user dan direktori

```bash
# Buat user non-root khusus untuk service
sudo useradd -r -s /bin/false userbot

# Buat direktori aplikasi
sudo mkdir -p /opt/userbot-promotion
sudo chown userbot:userbot /opt/userbot-promotion
```

### 2. Build dan install

```bash
# Build binary Linux
make build

# Copy binary dan session ke server (dari mesin lokal)
scp bin/userbot-promotion user@server:/usr/local/bin/
scp session.json user@server:/opt/userbot-promotion/   # jika sudah pernah login
```

### 3. Install service

```bash
# Di server
sudo make install-service
# atau manual:
sudo cp deploy/userbot-promotion.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable userbot-promotion
sudo systemctl start userbot-promotion
```

### 4. Perintah service

```bash
# Status
systemctl status userbot-promotion

# Log real-time
journalctl -u userbot-promotion -f

# Restart (misalnya setelah update binary)
systemctl restart userbot-promotion

# Stop
systemctl stop userbot-promotion
```

### 5. Update binary

```bash
make build
scp bin/userbot-promotion user@server:/usr/local/bin/
ssh user@server "systemctl restart userbot-promotion"
```

---

## Cara Kerja Scheduler

### Random Slot Scheduling

Scheduler tidak menggunakan interval tetap (setiap N jam). Sebagai gantinya,
setiap 24 jam scheduler membangun jadwal baru:

1. Window 24 jam dibagi menjadi **N slot** yang sama besar (N = limit grup).
2. Di setiap slot, **satu waktu acak** dipilih menggunakan `crypto/rand` (thread-safe, OS entropy).
3. Pesan dikirim di waktu yang dipilih — tersebar natural sepanjang hari.

**Contoh** — grup dengan `limit_24h = 3`:

```
Slot 0: 00:00 – 08:00  →  kirim pukul 02:47  ←─ acak
Slot 1: 08:00 – 16:00  →  kirim pukul 11:23  ←─ acak
Slot 2: 16:00 – 24:00  →  kirim pukul 19:51  ←─ acak
```

Jadwal lengkap dicetak di log setiap awal siklus untuk keperluan monitoring.

### Worker Pool

Pengiriman dijalankan oleh worker pool (5 goroutine paralel). Keuntungannya:
FloodWait di satu grup hanya memblokir satu worker — grup lain tetap berjalan normal.

### Re-check Eligibility

Saat waktunya tiba (bukan saat jadwal dibuat), scheduler selalu memeriksa ulang:
- Apakah grup masih di-whitelist dan tidak di-blacklist?
- Apakah pesan masih diizinkan untuk grup ini?
- Apakah limit harian belum tercapai?

Ini memastikan perubahan config yang dilakukan via API (tanpa restart) langsung
berlaku di slot berikutnya tanpa harus menunggu siklus 24 jam selesai.

---

## Cara Mendapatkan Group ID dan Topic ID

**Group ID:**
- Forward pesan dari grup ke [@userinfobot](https://t.me/userinfobot)
- Atau gunakan [@username_to_id_bot](https://t.me/username_to_id_bot)
- ID supergrup/channel selalu diawali `-100` (format Bot API)

**Topic ID:**
- Di Telegram Desktop: klik kanan nama topik → "Copy Link"
- Angka di akhir URL adalah topic_id

---

## Struktur Project

```
userbot-promotion/
├── cmd/
│   └── bot/
│       └── main.go                # Entry point — startup, shutdown, wiring
├── internal/
│   ├── api/
│   │   ├── handlers.go            # HTTP handler untuk semua endpoint
│   │   ├── middleware.go          # Bearer auth middleware
│   │   ├── response.go            # Helper response JSON standar
│   │   └── router.go              # Chi router + middleware stack
│   ├── db/
│   │   ├── db.go                  # Koneksi SQLite + migration runner
│   │   ├── models.go              # Struct model data
│   │   └── queries.go             # Semua query SQL
│   ├── scheduler/
│   │   └── scheduler.go           # Random scheduling + worker pool
│   └── telegram/
│       └── client.go              # Wrapper gotd/td + error handling
├── migrations/
│   ├── 001_init.sql               # Skema database lengkap
│   └── embed.go                   # Embed SQL ke binary
├── deploy/
│   └── userbot-promotion.service  # systemd unit file
├── Makefile
├── go.mod
├── go.sum
└── README.md
```

---

## Catatan Keamanan

- **Ganti `api_secret`** segera setelah setup awal. Default `changeme` tidak melindungi API.
- **Jangan expose port API** ke internet langsung. Gunakan reverse proxy (nginx/caddy) dengan HTTPS, atau batasi akses via firewall (`ufw allow from <IP>`).
- **Jalankan sebagai non-root.** Service file sudah dikonfigurasi dengan `User=userbot` dan hardening systemd (`NoNewPrivileges`, `ProtectSystem`, `PrivateTmp`).
- **Backup `session.json` dan `sqlite.db`** secara berkala. Kehilangan `session.json` memerlukan login ulang via OTP.
- **Telegram ToS:** Gunakan hanya untuk mengirim ke grup yang Anda kelola atau sudah mendapat izin. Penggunaan untuk spam dapat mengakibatkan akun di-ban permanen.

---

## License

MIT License — lihat file [LICENSE](LICENSE).
