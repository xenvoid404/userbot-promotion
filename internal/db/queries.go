package db

import (
	"database/sql"
	"strconv"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Config
// ─────────────────────────────────────────────────────────────────────────────

// GetConfig mengambil satu nilai konfigurasi. Mengembalikan ("", nil) jika key
// tidak ditemukan.
func GetConfig(sqlDB *sql.DB, key string) (string, error) {
	var val string
	err := sqlDB.QueryRow(`SELECT value FROM config WHERE key = ?`, key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

// SetConfig menyimpan atau memperbarui nilai konfigurasi (upsert).
func SetConfig(sqlDB *sql.DB, key, value string) error {
	_, err := sqlDB.Exec(`
		INSERT INTO config (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			value      = excluded.value,
			updated_at = excluded.updated_at`,
		key, value, time.Now().Unix())
	return err
}

// GetAllConfig mengambil semua entri konfigurasi.
// Nilai sensitif (api_hash, session_string) diganti "***" sebelum dikembalikan
// agar tidak bocor via API response.
func GetAllConfig(sqlDB *sql.DB) ([]ConfigEntry, error) {
	rows, err := sqlDB.Query(`SELECT key, value, updated_at FROM config ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []ConfigEntry
	for rows.Next() {
		var e ConfigEntry
		if err := rows.Scan(&e.Key, &e.Value, &e.UpdatedAt); err != nil {
			return nil, err
		}
		if e.Key == KeySessionString || e.Key == KeyAPIHash {
			e.Value = "***"
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// GetGlobalLimit membaca global_limit_24h dari config.
// Mengembalikan default 3 jika config tidak ada atau tidak valid.
func GetGlobalLimit(sqlDB *sql.DB) int {
	v, _ := GetConfig(sqlDB, KeyGlobalLimit24h)
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 3
	}
	return n
}

// ─────────────────────────────────────────────────────────────────────────────
// Messages
// ─────────────────────────────────────────────────────────────────────────────

// ListMessages mengembalikan semua pesan promosi beserta target masing-masing.
func ListMessages(sqlDB *sql.DB) ([]Message, error) {
	rows, err := sqlDB.Query(`
		SELECT id, name, text, media_path, delay_between_groups,
		       use_global_whitelist, active, created_at, updated_at
		FROM messages ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		m.Targets, _ = GetMessageTargets(sqlDB, m.ID)
		msgs = append(msgs, *m)
	}
	return msgs, rows.Err()
}

// GetMessage mengambil satu pesan berdasarkan ID beserta target-nya.
func GetMessage(sqlDB *sql.DB, id int64) (*Message, error) {
	row := sqlDB.QueryRow(`
		SELECT id, name, text, media_path, delay_between_groups,
		       use_global_whitelist, active, created_at, updated_at
		FROM messages WHERE id = ?`, id)
	m, err := scanMessage(row)
	if err != nil {
		return nil, err
	}
	m.Targets, _ = GetMessageTargets(sqlDB, m.ID)
	return m, nil
}

// CreateMessage membuat pesan baru beserta target-nya dalam satu transaksi
// atomik. INSERT messages + INSERT message_targets harus berhasil bersama
// atau rollback bersama — mencegah state inkonsisten.
func CreateMessage(sqlDB *sql.DB, m *Message) (int64, error) {
	tx, err := sqlDB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	res, err := tx.Exec(`
		INSERT INTO messages
			(name, text, media_path, delay_between_groups, use_global_whitelist, active)
		VALUES (?, ?, ?, ?, ?, 1)`,
		m.Name, m.Text, nullableString(m.MediaPath),
		m.DelayBetweenGroups, boolToInt(m.UseGlobalWhitelist))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := setMessageTargetsTx(tx, id, m.Targets); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdateMessage memperbarui pesan beserta target-nya dalam satu transaksi atomik.
func UpdateMessage(sqlDB *sql.DB, m *Message) error {
	tx, err := sqlDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.Exec(`
		UPDATE messages
		SET name=?, text=?, media_path=?, delay_between_groups=?,
		    use_global_whitelist=?, active=?, updated_at=?
		WHERE id=?`,
		m.Name, m.Text, nullableString(m.MediaPath),
		m.DelayBetweenGroups, boolToInt(m.UseGlobalWhitelist),
		boolToInt(m.Active), time.Now().Unix(), m.ID)
	if err != nil {
		return err
	}
	if err := setMessageTargetsTx(tx, m.ID, m.Targets); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteMessage menghapus pesan. Baris di message_targets dan group_messages
// terhapus otomatis karena ON DELETE CASCADE di migration.
func DeleteMessage(sqlDB *sql.DB, id int64) error {
	_, err := sqlDB.Exec(`DELETE FROM messages WHERE id = ?`, id)
	return err
}

// GetMessageTargets mengambil daftar group_id target untuk satu pesan.
func GetMessageTargets(sqlDB *sql.DB, messageID int64) ([]int64, error) {
	rows, err := sqlDB.Query(
		`SELECT group_id FROM message_targets WHERE message_id = ? ORDER BY group_id`,
		messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var targets []int64
	for rows.Next() {
		var gid int64
		if err := rows.Scan(&gid); err != nil {
			return nil, err
		}
		targets = append(targets, gid)
	}
	return targets, rows.Err()
}

// SetMessageTargets mengganti seluruh target pesan dalam transaksi baru.
func SetMessageTargets(sqlDB *sql.DB, messageID int64, groupIDs []int64) error {
	tx, err := sqlDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := setMessageTargetsTx(tx, messageID, groupIDs); err != nil {
		return err
	}
	return tx.Commit()
}

// setMessageTargetsTx adalah implementasi internal yang menerima *sql.Tx
// sehingga dapat digabung ke dalam transaksi yang sudah ada (reuse tx).
func setMessageTargetsTx(tx *sql.Tx, messageID int64, groupIDs []int64) error {
	if _, err := tx.Exec(
		`DELETE FROM message_targets WHERE message_id = ?`, messageID); err != nil {
		return err
	}
	for _, gid := range groupIDs {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO message_targets (message_id, group_id) VALUES (?, ?)`,
			messageID, gid); err != nil {
			return err
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Groups
// ─────────────────────────────────────────────────────────────────────────────

// ListGroups mengembalikan semua grup beserta allowed_messages masing-masing.
func ListGroups(sqlDB *sql.DB) ([]Group, error) {
	rows, err := sqlDB.Query(`
		SELECT group_id, label, topic_id, limit_24h, blacklisted, created_at, updated_at
		FROM groups ORDER BY group_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		g.AllowedMessages, _ = GetGroupAllowedMessages(sqlDB, g.GroupID)
		groups = append(groups, *g)
	}
	return groups, rows.Err()
}

// GetGroup mengambil satu grup berdasarkan group_id beserta allowed_messages-nya.
func GetGroup(sqlDB *sql.DB, groupID int64) (*Group, error) {
	row := sqlDB.QueryRow(`
		SELECT group_id, label, topic_id, limit_24h, blacklisted, created_at, updated_at
		FROM groups WHERE group_id = ?`, groupID)
	g, err := scanGroup(row)
	if err != nil {
		return nil, err
	}
	g.AllowedMessages, _ = GetGroupAllowedMessages(sqlDB, g.GroupID)
	return g, nil
}

// UpsertGroup membuat atau memperbarui satu grup (INSERT OR UPDATE).
func UpsertGroup(sqlDB *sql.DB, g *Group) error {
	_, err := sqlDB.Exec(`
		INSERT INTO groups (group_id, label, topic_id, limit_24h, blacklisted, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(group_id) DO UPDATE SET
			label       = excluded.label,
			topic_id    = excluded.topic_id,
			limit_24h   = excluded.limit_24h,
			blacklisted = excluded.blacklisted,
			updated_at  = excluded.updated_at`,
		g.GroupID, g.Label,
		nullableInt64(g.TopicID), nullableInt(g.Limit24h),
		boolToInt(g.Blacklisted), time.Now().Unix())
	return err
}

// SetGroupBlacklisted mengubah status blacklist satu grup.
func SetGroupBlacklisted(sqlDB *sql.DB, groupID int64, blacklisted bool) error {
	_, err := sqlDB.Exec(
		`UPDATE groups SET blacklisted=?, updated_at=? WHERE group_id=?`,
		boolToInt(blacklisted), time.Now().Unix(), groupID)
	return err
}

// DeleteGroup menghapus grup. Baris di group_messages terhapus otomatis (CASCADE).
func DeleteGroup(sqlDB *sql.DB, groupID int64) error {
	_, err := sqlDB.Exec(`DELETE FROM groups WHERE group_id = ?`, groupID)
	return err
}

// WhitelistedGroups mengembalikan semua grup yang tidak di-blacklist.
// Digunakan oleh scheduler untuk menentukan target pengiriman global.
func WhitelistedGroups(sqlDB *sql.DB) ([]Group, error) {
	rows, err := sqlDB.Query(`
		SELECT group_id, label, topic_id, limit_24h, blacklisted, created_at, updated_at
		FROM groups WHERE blacklisted = 0 ORDER BY group_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, *g)
	}
	return groups, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Group Allowed Messages
// ─────────────────────────────────────────────────────────────────────────────

// GetGroupAllowedMessages mengambil daftar message_id yang diizinkan untuk grup.
// Slice kosong berarti semua pesan diizinkan (tidak ada filter aktif).
func GetGroupAllowedMessages(sqlDB *sql.DB, groupID int64) ([]int64, error) {
	rows, err := sqlDB.Query(
		`SELECT message_id FROM group_messages WHERE group_id = ? ORDER BY message_id`,
		groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetGroupAllowedMessages mengganti seluruh filter pesan grup dalam satu transaksi.
// messageIDs kosong = hapus semua filter (izinkan semua pesan).
func SetGroupAllowedMessages(sqlDB *sql.DB, groupID int64, messageIDs []int64) error {
	tx, err := sqlDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(
		`DELETE FROM group_messages WHERE group_id = ?`, groupID); err != nil {
		return err
	}
	for _, mid := range messageIDs {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO group_messages (group_id, message_id) VALUES (?, ?)`,
			groupID, mid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// IsMessageAllowedForGroup memeriksa apakah pesan boleh dikirim ke grup ini.
//
// Logika:
//  1. Jika tidak ada filter aktif (group_messages kosong untuk group_id ini)
//     → semua pesan diizinkan → return true.
//  2. Jika ada filter → cek apakah message_id ada dalam filter.
func IsMessageAllowedForGroup(sqlDB *sql.DB, groupID, messageID int64) (bool, error) {
	var filterCount int
	if err := sqlDB.QueryRow(
		`SELECT COUNT(*) FROM group_messages WHERE group_id = ?`, groupID,
	).Scan(&filterCount); err != nil {
		return false, err
	}
	if filterCount == 0 {
		return true, nil // tidak ada filter → semua boleh
	}

	var allowed int
	err := sqlDB.QueryRow(
		`SELECT COUNT(*) FROM group_messages WHERE group_id = ? AND message_id = ?`,
		groupID, messageID,
	).Scan(&allowed)
	return allowed > 0, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Rate Limiting (via send_log)
// ─────────────────────────────────────────────────────────────────────────────

// CountSent24h menghitung jumlah pengiriman sukses ke grup dalam 24 jam terakhir.
func CountSent24h(sqlDB *sql.DB, groupID int64) (int, error) {
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	var count int
	err := sqlDB.QueryRow(`
		SELECT COUNT(*) FROM send_log
		WHERE group_id = ? AND status = 'ok' AND sent_at >= ?`,
		groupID, cutoff).Scan(&count)
	return count, err
}

// GetEffectiveLimit mengembalikan limit 24h efektif untuk grup.
// Prioritas: per-grup limit (limit_24h) → global limit dari config.
func GetEffectiveLimit(sqlDB *sql.DB, groupID int64) (int, error) {
	g, err := GetGroup(sqlDB, groupID)
	if err != nil {
		return GetGlobalLimit(sqlDB), nil
	}
	if g.Limit24h != nil {
		return *g.Limit24h, nil
	}
	return GetGlobalLimit(sqlDB), nil
}

// CanSend memeriksa apakah grup masih bisa menerima pesan hari ini.
func CanSend(sqlDB *sql.DB, groupID int64) (bool, error) {
	limit, err := GetEffectiveLimit(sqlDB, groupID)
	if err != nil {
		return false, err
	}
	sent, err := CountSent24h(sqlDB, groupID)
	if err != nil {
		return false, err
	}
	return sent < limit, nil
}

// RecordSend mencatat satu entri log pengiriman ke tabel send_log.
func RecordSend(sqlDB *sql.DB, groupID, messageID int64, status, note string) error {
	_, err := sqlDB.Exec(`
		INSERT INTO send_log (group_id, message_id, status, note, sent_at)
		VALUES (?, ?, ?, ?, ?)`,
		groupID, messageID, status, note, time.Now().Unix())
	return err
}

// GetGroupRateStatus mengambil status rate limit semua grup non-blacklist
// dalam satu query JOIN — menghindari N+1 query yang ada di versi sebelumnya.
func GetGroupRateStatus(sqlDB *sql.DB) ([]RateLimitStatus, error) {
	globalLimit := GetGlobalLimit(sqlDB)
	cutoff := time.Now().Add(-24 * time.Hour).Unix()

	rows, err := sqlDB.Query(`
		SELECT
			g.group_id,
			g.label,
			g.limit_24h,
			COUNT(CASE WHEN sl.status = 'ok' AND sl.sent_at >= ? THEN 1 END) AS sent_24h,
			MIN(CASE WHEN sl.status = 'ok' AND sl.sent_at >= ? THEN sl.sent_at END) AS oldest_ts
		FROM groups g
		LEFT JOIN send_log sl ON sl.group_id = g.group_id
		WHERE g.blacklisted = 0
		GROUP BY g.group_id
		ORDER BY g.group_id`,
		cutoff, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []RateLimitStatus
	for rows.Next() {
		var (
			groupID  int64
			label    string
			limit24h sql.NullInt64
			sent24h  int
			oldestTS sql.NullInt64
		)
		if err := rows.Scan(&groupID, &label, &limit24h, &sent24h, &oldestTS); err != nil {
			return nil, err
		}

		limit := globalLimit
		if limit24h.Valid && limit24h.Int64 > 0 {
			limit = int(limit24h.Int64)
		}

		remaining := limit - sent24h
		if remaining < 0 {
			remaining = 0
		}

		var nextAvailable int64
		if sent24h >= limit && oldestTS.Valid {
			nextAvailable = oldestTS.Int64 + 86400
		}

		result = append(result, RateLimitStatus{
			GroupID:       groupID,
			Label:         label,
			Limit24h:      limit,
			Sent24h:       sent24h,
			Remaining:     remaining,
			NextAvailable: nextAvailable,
			WindowStart:   cutoff,
		})
	}
	return result, rows.Err()
}

// ResetGroupRateLimit menghapus semua log pengiriman dalam 24 jam terakhir
// untuk satu grup, sehingga bot dapat kembali mengirim sebelum window berakhir.
func ResetGroupRateLimit(sqlDB *sql.DB, groupID int64) error {
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	_, err := sqlDB.Exec(
		`DELETE FROM send_log WHERE group_id = ? AND sent_at >= ?`, groupID, cutoff)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// Logs & Stats
// ─────────────────────────────────────────────────────────────────────────────

// ListLogs mengembalikan log pengiriman terbaru, diurutkan descending.
// limit <= 0 menggunakan default 100.
func ListLogs(sqlDB *sql.DB, limit int) ([]SendLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := sqlDB.Query(`
		SELECT id, group_id, message_id, status, note, sent_at
		FROM send_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []SendLog
	for rows.Next() {
		var l SendLog
		var note sql.NullString
		if err := rows.Scan(
			&l.ID, &l.GroupID, &l.MessageID, &l.Status, &note, &l.SentAt); err != nil {
			return nil, err
		}
		l.Note = note.String
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

// GetStats mengambil statistik pengiriman sukses dikelompokkan per (grup, pesan).
// since = 0 berarti semua waktu; since > 0 berarti filter unix timestamp.
func GetStats(sqlDB *sql.DB, since int64) ([]SendStats, error) {
	query := `
		SELECT
			sl.group_id,
			COALESCE(g.label, ''),
			sl.message_id,
			COALESCE(m.name, ''),
			COUNT(*) AS cnt
		FROM send_log sl
		LEFT JOIN groups   g ON g.group_id = sl.group_id
		LEFT JOIN messages m ON m.id       = sl.message_id
		WHERE sl.status = 'ok'`

	args := []any{}
	if since > 0 {
		query += ` AND sl.sent_at >= ?`
		args = append(args, since)
	}
	query += ` GROUP BY sl.group_id, sl.message_id ORDER BY cnt DESC`

	rows, err := sqlDB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []SendStats
	for rows.Next() {
		var s SendStats
		if err := rows.Scan(
			&s.GroupID, &s.Label, &s.MessageID, &s.MessageName, &s.Count); err != nil {
			return nil, err
		}
		stats = append(stats, s)
	}
	return stats, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal scan helpers
// ─────────────────────────────────────────────────────────────────────────────

// rowScanner adalah interface yang dipenuhi oleh *sql.Row dan *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanMessage membaca satu baris dari query messages.
func scanMessage(s rowScanner) (*Message, error) {
	var m Message
	var useGlobal, active int
	var mediaPath sql.NullString

	if err := s.Scan(
		&m.ID, &m.Name, &m.Text, &mediaPath,
		&m.DelayBetweenGroups, &useGlobal, &active,
		&m.CreatedAt, &m.UpdatedAt,
	); err != nil {
		return nil, err
	}

	m.MediaPath = mediaPath.String
	m.UseGlobalWhitelist = useGlobal == 1
	m.Active = active == 1
	return &m, nil
}

// scanGroup membaca satu baris dari query groups.
//
// Menggunakan sql.NullInt64 untuk kolom nullable (topic_id, limit_24h).
// Scan langsung ke *int64 akan mengembalikan error "converting NULL to *int64"
// saat nilai kolom adalah NULL.
func scanGroup(s rowScanner) (*Group, error) {
	var g Group
	var blacklisted int
	var topicID, limit24h sql.NullInt64

	if err := s.Scan(
		&g.GroupID, &g.Label, &topicID, &limit24h,
		&blacklisted, &g.CreatedAt, &g.UpdatedAt,
	); err != nil {
		return nil, err
	}

	if topicID.Valid {
		v := topicID.Int64
		g.TopicID = &v
	}
	if limit24h.Valid {
		v := int(limit24h.Int64)
		g.Limit24h = &v
	}
	g.Blacklisted = blacklisted == 1
	return &g, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Value helpers
// ─────────────────────────────────────────────────────────────────────────────

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullableInt64(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func nullableInt(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}
