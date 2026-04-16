package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/xenvoid404/userbot-promotion/internal/db"
)

// maxBodyBytes adalah batas ukuran request body.
// Mencegah DoS via payload yang sangat besar.
const maxBodyBytes = 1 << 20 // 1 MB

// Handler menyimpan dependency yang dibutuhkan semua HTTP handler.
type Handler struct {
	db       *sql.DB
	reloadFn func() // dipanggil setelah mutasi config/pesan agar scheduler reload
}

// NewHandler membuat instance Handler baru.
func NewHandler(sqlDB *sql.DB, reloadFn func()) *Handler {
	return &Handler{db: sqlDB, reloadFn: reloadFn}
}

// decode membaca dan mendekode JSON dari request body dengan pembatasan ukuran.
// DisallowUnknownFields mencegah client mengirim field yang tidak dikenal
// dan memberikan error yang informatif jika ada typo nama field.
func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	// Pastikan tidak ada konten ekstra setelah objek JSON pertama.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("request body harus berisi tepat satu objek JSON")
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Health
// ─────────────────────────────────────────────────────────────────────────────

// GET /health — tidak membutuhkan autentikasi; digunakan untuk health check
// dan monitoring uptime (systemd, uptime-kuma, dll).
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	OK(w, map[string]string{"status": "ok"})
}

// ─────────────────────────────────────────────────────────────────────────────
// Config
// ─────────────────────────────────────────────────────────────────────────────

// GET /config
// Mengembalikan semua entri config. Nilai sensitif (api_hash, session_string)
// diganti "***" agar tidak bocor via response.
func (h *Handler) ListConfig(w http.ResponseWriter, r *http.Request) {
	entries, err := db.GetAllConfig(h.db)
	if err != nil {
		InternalErr(w, err)
		return
	}
	if entries == nil {
		entries = []db.ConfigEntry{}
	}
	OK(w, entries)
}

// PUT /config
// Body: {"key": "global_limit_24h", "value": "5"}
func (h *Handler) SetConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := decode(r, &body); err != nil {
		BadRequest(w, "body tidak valid: "+err.Error())
		return
	}
	if body.Key == "" {
		BadRequest(w, "field 'key' wajib diisi")
		return
	}
	// session_string hanya boleh diubah oleh client Telegram secara internal.
	if body.Key == db.KeySessionString {
		BadRequest(w, "session_string tidak dapat diubah via API")
		return
	}
	if err := db.SetConfig(h.db, body.Key, body.Value); err != nil {
		InternalErr(w, err)
		return
	}
	// Reload scheduler jika parameter yang mempengaruhi jadwal berubah.
	if body.Key == db.KeyGlobalLimit24h {
		h.reloadFn()
	}
	OK(w, nil)
}

// ─────────────────────────────────────────────────────────────────────────────
// Messages
// ─────────────────────────────────────────────────────────────────────────────

// GET /messages
func (h *Handler) ListMessages(w http.ResponseWriter, r *http.Request) {
	msgs, err := db.ListMessages(h.db)
	if err != nil {
		InternalErr(w, err)
		return
	}
	if msgs == nil {
		msgs = []db.Message{}
	}
	OK(w, msgs)
}

// POST /messages
func (h *Handler) CreateMessage(w http.ResponseWriter, r *http.Request) {
	var m db.Message
	if err := decode(r, &m); err != nil {
		BadRequest(w, "body tidak valid: "+err.Error())
		return
	}
	if m.Name == "" {
		BadRequest(w, "field 'name' wajib diisi")
		return
	}
	if m.Text == "" && m.MediaPath == "" {
		BadRequest(w, "salah satu dari 'text' atau 'media_path' wajib diisi")
		return
	}
	if m.DelayBetweenGroups <= 0 {
		m.DelayBetweenGroups = 5
	}

	id, err := db.CreateMessage(h.db, &m)
	if err != nil {
		InternalErr(w, err)
		return
	}

	// Re-fetch dari DB untuk mendapatkan nilai yang di-generate DB
	// (created_at, updated_at, active default, dll) — bukan data stale dari request.
	created, err := db.GetMessage(h.db, id)
	if err != nil {
		InternalErr(w, err)
		return
	}

	h.reloadFn()
	Created(w, created)
}

// GET /messages/{id}
func (h *Handler) GetMessage(w http.ResponseWriter, r *http.Request) {
	id, err := paramInt64(r, "id")
	if err != nil {
		BadRequest(w, "id tidak valid")
		return
	}
	m, err := db.GetMessage(h.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		NotFound(w)
		return
	} else if err != nil {
		InternalErr(w, err)
		return
	}
	OK(w, m)
}

// PUT /messages/{id}
func (h *Handler) UpdateMessage(w http.ResponseWriter, r *http.Request) {
	id, err := paramInt64(r, "id")
	if err != nil {
		BadRequest(w, "id tidak valid")
		return
	}
	var m db.Message
	if err := decode(r, &m); err != nil {
		BadRequest(w, "body tidak valid: "+err.Error())
		return
	}
	m.ID = id

	if err := db.UpdateMessage(h.db, &m); err != nil {
		InternalErr(w, err)
		return
	}

	// Re-fetch untuk memastikan response mencerminkan state DB yang sebenarnya,
	// bukan data dari request body.
	updated, err := db.GetMessage(h.db, id)
	if err != nil {
		InternalErr(w, err)
		return
	}

	h.reloadFn()
	OK(w, updated)
}

// DELETE /messages/{id}
func (h *Handler) DeleteMessage(w http.ResponseWriter, r *http.Request) {
	id, err := paramInt64(r, "id")
	if err != nil {
		BadRequest(w, "id tidak valid")
		return
	}
	if err := db.DeleteMessage(h.db, id); err != nil {
		InternalErr(w, err)
		return
	}
	h.reloadFn()
	NoContent(w)
}

// ─────────────────────────────────────────────────────────────────────────────
// Groups
// ─────────────────────────────────────────────────────────────────────────────

// GET /groups
func (h *Handler) ListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := db.ListGroups(h.db)
	if err != nil {
		InternalErr(w, err)
		return
	}
	if groups == nil {
		groups = []db.Group{}
	}
	OK(w, groups)
}

// POST /groups — whitelist atau update grup.
func (h *Handler) UpsertGroup(w http.ResponseWriter, r *http.Request) {
	var g db.Group
	if err := decode(r, &g); err != nil {
		BadRequest(w, "body tidak valid: "+err.Error())
		return
	}
	if g.GroupID == 0 {
		BadRequest(w, "field 'group_id' wajib diisi")
		return
	}

	if err := db.UpsertGroup(h.db, &g); err != nil {
		InternalErr(w, err)
		return
	}

	// Jika allowed_messages disertakan dalam request, set sekarang dan
	// propagasi error dengan benar (tidak silent drop).
	if len(g.AllowedMessages) > 0 {
		if err := db.SetGroupAllowedMessages(h.db, g.GroupID, g.AllowedMessages); err != nil {
			InternalErr(w, err)
			return
		}
	}

	created, err := db.GetGroup(h.db, g.GroupID)
	if err != nil {
		InternalErr(w, err)
		return
	}
	Created(w, created)
}

// GET /groups/{id}
func (h *Handler) GetGroup(w http.ResponseWriter, r *http.Request) {
	id, err := paramInt64(r, "id")
	if err != nil {
		BadRequest(w, "id tidak valid")
		return
	}
	g, err := db.GetGroup(h.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		NotFound(w)
		return
	} else if err != nil {
		InternalErr(w, err)
		return
	}
	OK(w, g)
}

// PUT /groups/{id}
func (h *Handler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	id, err := paramInt64(r, "id")
	if err != nil {
		BadRequest(w, "id tidak valid")
		return
	}
	var g db.Group
	if err := decode(r, &g); err != nil {
		BadRequest(w, "body tidak valid: "+err.Error())
		return
	}
	g.GroupID = id
	if err := db.UpsertGroup(h.db, &g); err != nil {
		InternalErr(w, err)
		return
	}
	updated, err := db.GetGroup(h.db, id)
	if err != nil {
		InternalErr(w, err)
		return
	}
	OK(w, updated)
}

// DELETE /groups/{id}
func (h *Handler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, err := paramInt64(r, "id")
	if err != nil {
		BadRequest(w, "id tidak valid")
		return
	}
	if err := db.DeleteGroup(h.db, id); err != nil {
		InternalErr(w, err)
		return
	}
	NoContent(w)
}

// POST /groups/{id}/blacklist
// Body (opsional): {"blacklisted": true}  — default true jika body kosong.
func (h *Handler) BlacklistGroup(w http.ResponseWriter, r *http.Request) {
	id, err := paramInt64(r, "id")
	if err != nil {
		BadRequest(w, "id tidak valid")
		return
	}
	body := struct {
		Blacklisted bool `json:"blacklisted"`
	}{Blacklisted: true}
	_ = decode(r, &body) // opsional — abaikan error jika body kosong

	if err := db.SetGroupBlacklisted(h.db, id, body.Blacklisted); err != nil {
		InternalErr(w, err)
		return
	}
	OK(w, map[string]any{"group_id": id, "blacklisted": body.Blacklisted})
}

// GET /groups/{id}/messages — lihat filter pesan aktif untuk grup ini.
func (h *Handler) GetGroupMessages(w http.ResponseWriter, r *http.Request) {
	id, err := paramInt64(r, "id")
	if err != nil {
		BadRequest(w, "id tidak valid")
		return
	}
	ids, err := db.GetGroupAllowedMessages(h.db, id)
	if err != nil {
		InternalErr(w, err)
		return
	}
	if ids == nil {
		ids = []int64{}
	}
	OK(w, map[string]any{
		"group_id":         id,
		"allowed_messages": ids,
		"note":             "kosong = semua pesan diizinkan",
	})
}

// PUT /groups/{id}/messages — set filter pesan untuk grup ini.
// Body: {"message_ids": [1, 3]}  — array kosong = izinkan semua pesan.
func (h *Handler) SetGroupMessages(w http.ResponseWriter, r *http.Request) {
	id, err := paramInt64(r, "id")
	if err != nil {
		BadRequest(w, "id tidak valid")
		return
	}
	var body struct {
		MessageIDs []int64 `json:"message_ids"`
	}
	if err := decode(r, &body); err != nil {
		BadRequest(w, "body tidak valid: "+err.Error())
		return
	}
	if err := db.SetGroupAllowedMessages(h.db, id, body.MessageIDs); err != nil {
		InternalErr(w, err)
		return
	}
	if body.MessageIDs == nil {
		body.MessageIDs = []int64{}
	}
	OK(w, map[string]any{
		"group_id":         id,
		"allowed_messages": body.MessageIDs,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Rate Limit
// ─────────────────────────────────────────────────────────────────────────────

// GET /ratelimit — status rate limit semua grup non-blacklist.
func (h *Handler) GetRateLimit(w http.ResponseWriter, r *http.Request) {
	status, err := db.GetGroupRateStatus(h.db)
	if err != nil {
		InternalErr(w, err)
		return
	}
	if status == nil {
		status = []db.RateLimitStatus{}
	}
	OK(w, status)
}

// DELETE /ratelimit/{id} — reset rate limit satu grup (paksa bisa kirim lagi hari ini).
func (h *Handler) ResetRateLimit(w http.ResponseWriter, r *http.Request) {
	id, err := paramInt64(r, "id")
	if err != nil {
		BadRequest(w, "id tidak valid")
		return
	}
	if err := db.ResetGroupRateLimit(h.db, id); err != nil {
		InternalErr(w, err)
		return
	}
	OK(w, map[string]any{"group_id": id, "reset": true})
}

// ─────────────────────────────────────────────────────────────────────────────
// Logs & Stats
// ─────────────────────────────────────────────────────────────────────────────

// GET /logs?limit=100
func (h *Handler) ListLogs(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	logs, err := db.ListLogs(h.db, limit)
	if err != nil {
		InternalErr(w, err)
		return
	}
	if logs == nil {
		logs = []db.SendLog{}
	}
	OK(w, logs)
}

// GET /logs/stats?since=7d
// Query param since: "24h" | "7d" | "30d" | unix_timestamp_integer
func (h *Handler) GetStats(w http.ResponseWriter, r *http.Request) {
	var since int64
	if s := r.URL.Query().Get("since"); s != "" {
		switch s {
		case "24h":
			since = time.Now().Add(-24 * time.Hour).Unix()
		case "7d":
			since = time.Now().Add(-7 * 24 * time.Hour).Unix()
		case "30d":
			since = time.Now().Add(-30 * 24 * time.Hour).Unix()
		default:
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				since = n
			}
		}
	}
	stats, err := db.GetStats(h.db, since)
	if err != nil {
		InternalErr(w, err)
		return
	}
	if stats == nil {
		stats = []db.SendStats{}
	}
	OK(w, stats)
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func paramInt64(r *http.Request, key string) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, key), 10, 64)
}
