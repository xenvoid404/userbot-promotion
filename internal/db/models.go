// Package db mendefinisikan model data, konstanta konfigurasi, dan semua
// query ke database SQLite.
package db

// ─── Config key constants ─────────────────────────────────────────────────────
// Menggunakan konstanta mencegah typo string dan memudahkan refactor.
const (
	KeyAPIID          = "api_id"
	KeyAPIHash        = "api_hash"
	KeyPhoneNumber    = "phone_number"
	KeySessionString  = "session_string"
	KeyGlobalLimit24h = "global_limit_24h"
	KeyAPIPort        = "api_port"
	KeyAPISecret      = "api_secret"
)

// ConfigEntry merepresentasikan satu baris di tabel config.
type ConfigEntry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt int64  `json:"updated_at"`
}

// Message merepresentasikan satu pesan promosi beserta konfigurasinya.
//   - Targets hanya diisi jika UseGlobalWhitelist = false.
//   - MediaPath kosong berarti pesan hanya teks.
type Message struct {
	ID                 int64   `json:"id"`
	Name               string  `json:"name"`
	Text               string  `json:"text"`
	MediaPath          string  `json:"media_path"`
	DelayBetweenGroups int     `json:"delay_between_groups"`
	UseGlobalWhitelist bool    `json:"use_global_whitelist"`
	Active             bool    `json:"active"`
	Targets            []int64 `json:"targets,omitempty"`
	CreatedAt          int64   `json:"created_at"`
	UpdatedAt          int64   `json:"updated_at"`
}

// Group merepresentasikan satu grup Telegram yang telah di-whitelist.
//   - TopicID    nil  = kirim ke chat utama; non-nil = topik supergrup
//   - Limit24h   nil  = ikuti global_limit_24h; non-nil = limit khusus grup ini
//   - AllowedMessages nil/kosong = semua pesan diizinkan; berisi ID = filter aktif
type Group struct {
	GroupID         int64   `json:"group_id"`
	Label           string  `json:"label"`
	TopicID         *int64  `json:"topic_id"`
	Limit24h        *int    `json:"limit_24h"`
	Blacklisted     bool    `json:"blacklisted"`
	AllowedMessages []int64 `json:"allowed_messages,omitempty"`
	CreatedAt       int64   `json:"created_at"`
	UpdatedAt       int64   `json:"updated_at"`
}

// SendLog merepresentasikan satu entri log pengiriman.
// Status: "ok" | "flood_wait" | "forbidden" | "error"
type SendLog struct {
	ID        int64  `json:"id"`
	GroupID   int64  `json:"group_id"`
	MessageID int64  `json:"message_id"`
	Status    string `json:"status"`
	Note      string `json:"note"`
	SentAt    int64  `json:"sent_at"`
}

// SendStats adalah agregasi statistik pengiriman per (grup, pesan).
type SendStats struct {
	GroupID     int64  `json:"group_id"`
	Label       string `json:"label"`
	MessageID   int64  `json:"message_id"`
	MessageName string `json:"message_name"`
	Count       int    `json:"count"`
}

// RateLimitStatus adalah status rate limit satu grup untuk endpoint GET /ratelimit.
type RateLimitStatus struct {
	GroupID       int64  `json:"group_id"`
	Label         string `json:"label"`
	Limit24h      int    `json:"limit_24h"`
	Sent24h       int    `json:"sent_24h"`
	Remaining     int    `json:"remaining"`
	NextAvailable int64  `json:"next_available"` // unix timestamp; 0 = belum mencapai limit
	WindowStart   int64  `json:"window_start"`   // unix timestamp awal window 24 jam
}
