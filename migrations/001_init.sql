-- =============================================================================
-- migrations/001_init.sql
-- Skema database lengkap untuk userbot-promotion.
-- Dieksekusi otomatis saat binary pertama kali dijalankan.
-- Semua statement menggunakan IF NOT EXISTS sehingga aman dijalankan ulang.
-- =============================================================================

PRAGMA journal_mode = WAL;       -- concurrent reader + satu writer, performa lebih baik
PRAGMA foreign_keys = ON;        -- enforce referential integrity (CASCADE, dll)

-- -----------------------------------------------------------------------------
-- config: konfigurasi global dalam format key-value
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS config (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
);

-- Nilai default; ON CONFLICT IGNORE agar tidak menimpa config yang sudah ada.
INSERT OR IGNORE INTO config (key, value) VALUES
    ('api_id',             ''),
    ('api_hash',           ''),
    ('phone_number',       ''),
    ('session_string',     ''),
    ('global_limit_24h',   '3'),
    ('api_port',           '8080'),
    ('api_secret',         'changeme');

-- -----------------------------------------------------------------------------
-- messages: pesan promosi yang akan dikirim secara terjadwal
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS messages (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    name                 TEXT    NOT NULL UNIQUE,
    text                 TEXT    NOT NULL DEFAULT '',
    media_path           TEXT,                           -- NULL = hanya teks
    delay_between_groups INTEGER NOT NULL DEFAULT 5,     -- detik jeda antar grup
    use_global_whitelist INTEGER NOT NULL DEFAULT 1,     -- 1=pakai whitelist global
    active               INTEGER NOT NULL DEFAULT 1,     -- 1=aktif, 0=nonaktif
    created_at           INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    updated_at           INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
);

-- -----------------------------------------------------------------------------
-- message_targets: target grup khusus jika use_global_whitelist = 0
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS message_targets (
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    group_id   INTEGER NOT NULL,
    PRIMARY KEY (message_id, group_id)
);

-- -----------------------------------------------------------------------------
-- groups: daftar grup yang telah di-whitelist
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS groups (
    group_id    INTEGER PRIMARY KEY,
    label       TEXT    NOT NULL DEFAULT '',
    topic_id    INTEGER,          -- NULL = chat utama; non-NULL = topik supergrup
    limit_24h   INTEGER,          -- NULL = ikuti global_limit_24h dari config
    blacklisted INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    updated_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
);

-- -----------------------------------------------------------------------------
-- group_messages: filter pesan per grup
-- Jika tidak ada baris untuk group_id tertentu → semua pesan diizinkan.
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS group_messages (
    group_id   INTEGER NOT NULL REFERENCES groups(group_id)   ON DELETE CASCADE,
    message_id INTEGER NOT NULL REFERENCES messages(id)       ON DELETE CASCADE,
    PRIMARY KEY (group_id, message_id)
);

-- -----------------------------------------------------------------------------
-- send_log: log setiap percobaan pengiriman (sekaligus dipakai sebagai
-- rate-limit tracker — tidak ada tabel rate_limit terpisah)
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS send_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    group_id   INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    status     TEXT    NOT NULL DEFAULT 'ok', -- ok | flood_wait | forbidden | error
    note       TEXT    NOT NULL DEFAULT '',
    sent_at    INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
);

-- Index untuk query rate-limit (WHERE group_id=? AND status='ok' AND sent_at>=?)
CREATE INDEX IF NOT EXISTS idx_send_log_group_sent ON send_log (group_id, sent_at);
-- Index untuk query statistik per pesan
CREATE INDEX IF NOT EXISTS idx_send_log_message    ON send_log (message_id);
