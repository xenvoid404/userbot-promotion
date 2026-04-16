// Package scheduler mengatur siklus pengiriman pesan promosi dengan distribusi
// waktu acak dalam window 24 jam per grup.
package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/xenvoid404/userbot-promotion/internal/db"
	"github.com/xenvoid404/userbot-promotion/internal/telegram"
	"go.uber.org/zap"
)

const (
	// window adalah panjang satu siklus pengiriman (24 jam).
	window = 24 * time.Hour

	// maxWorkers adalah jumlah goroutine pengiriman paralel.
	// FloodWait di satu worker tidak memblokir worker lain.
	maxWorkers = 5
)

// job merepresentasikan satu pengiriman yang sudah dijadwalkan.
type job struct {
	absTime   time.Time // waktu absolut pengiriman
	groupID   int64
	messageID int64
	msgName   string
	delay     time.Duration // jeda setelah kirim sebelum ambil job berikutnya
	text      string
	mediaPath string
}

// Scheduler mengelola siklus pengiriman semua pesan promosi aktif.
// Semua method aman dipanggil dari beberapa goroutine secara bersamaan.
type Scheduler struct {
	sqlDB *sql.DB
	tg    *telegram.Client
	log   *zap.Logger

	mu      sync.Mutex         // melindungi cancel dan running
	cancel  context.CancelFunc // nil jika scheduler tidak berjalan
	running bool
	wg      sync.WaitGroup // menunggu semua goroutine selesai saat Stop()
}

// New membuat instance Scheduler baru. Belum memulai goroutine apapun.
func New(sqlDB *sql.DB, tg *telegram.Client, log *zap.Logger) *Scheduler {
	return &Scheduler{sqlDB: sqlDB, tg: tg, log: log}
}

// Start memulai scheduler. Jika sudah berjalan, instance lama dihentikan
// terlebih dahulu sebelum yang baru dimulai (idempotent).
//
// ── Kenapa urutan operasi ini penting (race condition prevention) ──────────
//
// Naif: Lock → cancel() → wg.Wait() → Lock lagi
// Problem: wg.Wait() di dalam Lock akan deadlock jika goroutine lama mencoba
// mengakses mutex (misalnya saat menulis ke DB atau log).
//
// Solusi: simpan cancel ke variabel lokal, unlock mutex, tunggu goroutine,
// baru lock kembali. Hanya satu caller yang bisa masuk ke blok "stop lama"
// karena caller berikutnya menunggu di Lock() di baris pertama.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()

	if s.running && s.cancel != nil {
		// Ambil cancel func dan tandai scheduler sebagai stopped.
		oldCancel := s.cancel
		s.cancel = nil
		s.running = false
		s.mu.Unlock() // ← unlock SEBELUM Wait() untuk mencegah deadlock

		oldCancel()
		s.wg.Wait() // tunggu goroutine lama benar-benar selesai

		s.mu.Lock() // lock kembali untuk set state baru
	}

	childCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.running = true

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.loop(childCtx)
	}()

	s.mu.Unlock()
}

// Stop menghentikan scheduler dan memblokir hingga semua goroutine selesai.
// Aman dipanggil meski scheduler belum pernah Start atau sudah Stop.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.running = false
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.wg.Wait() // blokir sampai loop + semua worker selesai → no goroutine leak
}

// ─────────────────────────────────────────────────────────────────────────────
// Main loop
// ─────────────────────────────────────────────────────────────────────────────

// loop adalah goroutine utama yang berulang setiap 24 jam.
func (s *Scheduler) loop(ctx context.Context) {
	for {
		cycleStart := time.Now()
		s.log.Info("⏰ Siklus baru dimulai", zap.Time("cycle_start", cycleStart))

		if err := s.runCycle(ctx, cycleStart); err != nil {
			if ctx.Err() != nil {
				return // context dibatalkan — keluar bersih
			}
			s.log.Error("❌ Error pada siklus", zap.Error(err))
		}

		elapsed := time.Since(cycleStart)
		wait := window - elapsed
		if wait < 0 {
			wait = 0
		}
		s.log.Info("✅ Siklus selesai",
			zap.Duration("elapsed", elapsed.Round(time.Second)),
			zap.Duration("next_in", wait.Round(time.Second)),
		)

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// runCycle membangun jadwal untuk satu siklus 24 jam lalu mengeksekusinya.
func (s *Scheduler) runCycle(ctx context.Context, cycleStart time.Time) error {
	messages, err := db.ListMessages(s.sqlDB)
	if err != nil {
		return fmt.Errorf("list messages: %w", err)
	}

	jobs, err := s.buildJobs(messages, cycleStart)
	if err != nil {
		return fmt.Errorf("build jobs: %w", err)
	}

	if len(jobs) == 0 {
		s.log.Info("ℹ️  Tidak ada job untuk siklus ini (tidak ada pesan aktif atau grup)")
		return nil
	}

	// Cetak jadwal lengkap di awal siklus untuk monitoring.
	s.log.Info("📋 Jadwal siklus ini", zap.Int("total_jobs", len(jobs)))
	for _, j := range jobs {
		s.log.Info("  →",
			zap.Int64("group_id", j.groupID),
			zap.String("message", j.msgName),
			zap.String("jam", j.absTime.Format("15:04:05")),
		)
	}

	s.executeJobs(ctx, jobs)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Job building
// ─────────────────────────────────────────────────────────────────────────────

// buildJobs membangun daftar job yang sudah diurutkan berdasarkan waktu absolut.
//
// Untuk setiap pasangan (pesan aktif, grup yang diizinkan):
//  1. Ambil limit efektif grup (per-grup atau global).
//  2. Bagi window 24 jam menjadi `limit` slot yang sama besar.
//  3. Pilih satu waktu acak di dalam setiap slot (crypto/rand).
func (s *Scheduler) buildJobs(messages []db.Message, cycleStart time.Time) ([]job, error) {
	var jobs []job

	for _, msg := range messages {
		if !msg.Active {
			continue
		}

		groupIDs, err := s.resolveTargets(msg)
		if err != nil {
			s.log.Error("Gagal resolve target",
				zap.String("message", msg.Name), zap.Error(err))
			continue
		}

		for _, gid := range groupIDs {
			allowed, err := db.IsMessageAllowedForGroup(s.sqlDB, gid, msg.ID)
			if err != nil {
				s.log.Error("Gagal cek filter pesan",
					zap.Int64("group_id", gid), zap.Error(err))
				continue
			}
			if !allowed {
				continue
			}

			limit, err := db.GetEffectiveLimit(s.sqlDB, gid)
			if err != nil {
				s.log.Error("Gagal ambil limit grup",
					zap.Int64("group_id", gid), zap.Error(err))
				continue
			}

			slots, err := randomSlots(limit, window, cycleStart)
			if err != nil {
				s.log.Error("Gagal generate random slots", zap.Error(err))
				continue
			}

			for _, t := range slots {
				jobs = append(jobs, job{
					absTime:   t,
					groupID:   gid,
					messageID: msg.ID,
					msgName:   msg.Name,
					delay:     time.Duration(msg.DelayBetweenGroups) * time.Second,
					text:      msg.Text,
					mediaPath: msg.MediaPath,
				})
			}
		}
	}

	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].absTime.Before(jobs[j].absTime)
	})

	return jobs, nil
}

// resolveTargets mengembalikan daftar group_id target untuk satu pesan.
func (s *Scheduler) resolveTargets(msg db.Message) ([]int64, error) {
	if !msg.UseGlobalWhitelist {
		return msg.Targets, nil
	}
	groups, err := db.WhitelistedGroups(s.sqlDB)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.GroupID)
	}
	return ids, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Worker pool
// ─────────────────────────────────────────────────────────────────────────────

// executeJobs mendistribusikan job ke worker pool dan menunggu semua selesai.
//
// ── Mengapa worker pool, bukan sequential loop ─────────────────────────────
// Pada loop sequential, FloodWait di satu grup memblokir seluruh antrian.
// Contoh: grup A kena FloodWait 300 detik → grup B-Z yang jadwalnya sudah
// tiba harus menunggu 300 detik ikut-ikutan. Dengan worker pool, setiap
// goroutine menangani job-nya sendiri — FloodWait hanya memblokir satu slot
// worker, worker lain tetap berjalan normal.
func (s *Scheduler) executeJobs(ctx context.Context, jobs []job) {
	jobCh := make(chan job, len(jobs))
	for _, j := range jobs {
		jobCh <- j
	}
	close(jobCh)

	// Batasi jumlah worker ke min(maxWorkers, len(jobs)) agar tidak spawn
	// goroutine yang tidak perlu.
	workers := maxWorkers
	if len(jobs) < workers {
		workers = len(jobs)
	}

	var workerWg sync.WaitGroup
	for i := 0; i < workers; i++ {
		workerWg.Add(1)
		go func() {
			defer workerWg.Done()
			s.worker(ctx, jobCh)
		}()
	}

	done := make(chan struct{})
	go func() {
		workerWg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
	case <-done:
	}
}

// worker mengambil job dari channel dan mengeksekusinya satu per satu.
func (s *Scheduler) worker(ctx context.Context, jobs <-chan job) {
	for {
		select {
		case <-ctx.Done():
			return
		case j, ok := <-jobs:
			if !ok {
				return
			}
			s.executeJob(ctx, j)
		}
	}
}

// executeJob menunggu waktu yang dijadwalkan, re-check eligibility,
// lalu mengirim pesan dan mencatat hasilnya.
func (s *Scheduler) executeJob(ctx context.Context, j job) {
	// Tidur hingga waktu pengiriman yang sudah dijadwalkan tiba.
	if now := time.Now(); j.absTime.After(now) {
		wait := j.absTime.Sub(now)
		s.log.Info("⏳ Menunggu jadwal",
			zap.Int64("group_id", j.groupID),
			zap.String("message", j.msgName),
			zap.String("jam", j.absTime.Format("15:04:05")),
			zap.Duration("tunggu", wait.Round(time.Second)),
		)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}

	// Re-check eligibility saat waktunya tiba.
	// Kondisi bisa berubah (blacklist, filter, limit) sejak jadwal dibuat
	// di awal siklus — sehingga tidak perlu restart scheduler tiap perubahan.
	g, err := db.GetGroup(s.sqlDB, j.groupID)
	if err != nil || g.Blacklisted {
		s.log.Info("⏭️  Dilewati",
			zap.Int64("group_id", j.groupID),
			zap.String("alasan", "blacklist atau tidak ditemukan"),
		)
		return
	}

	allowed, err := db.IsMessageAllowedForGroup(s.sqlDB, j.groupID, j.messageID)
	if err != nil || !allowed {
		return
	}

	canSend, err := db.CanSend(s.sqlDB, j.groupID)
	if err != nil || !canSend {
		limit, _ := db.GetEffectiveLimit(s.sqlDB, j.groupID)
		s.log.Info("🔒 Limit 24h tercapai",
			zap.Int64("group_id", j.groupID),
			zap.Int("limit", limit),
		)
		return
	}

	// Kirim dan catat hasilnya ke send_log.
	status, note := s.send(ctx, g, j.messageID, j.text, j.mediaPath)
	if err := db.RecordSend(s.sqlDB, j.groupID, j.messageID, status, note); err != nil {
		s.log.Error("Gagal catat send_log",
			zap.Int64("group_id", j.groupID), zap.Error(err))
	}

	if status == "ok" {
		limit, _ := db.GetEffectiveLimit(s.sqlDB, j.groupID)
		sent, _ := db.CountSent24h(s.sqlDB, j.groupID)
		s.log.Info("✅ Terkirim",
			zap.Int64("group_id", j.groupID),
			zap.String("message", j.msgName),
			zap.String("sisa", fmt.Sprintf("%d/%d", limit-sent, limit)),
		)
	}

	// Jeda antar pengiriman di worker ini (jika dikonfigurasi > 0).
	if j.delay > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(j.delay):
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Send
// ─────────────────────────────────────────────────────────────────────────────

// send melakukan pengiriman dan menangani error Telegram.
// Mengembalikan (status, note) untuk dicatat di send_log.
func (s *Scheduler) send(
	ctx context.Context, g *db.Group,
	messageID int64, text, mediaPath string,
) (status, note string) {
	var topicID int64
	if g.TopicID != nil {
		topicID = *g.TopicID
	}

	err := s.tg.SendMessage(ctx, g.GroupID, topicID, text, mediaPath)
	if err == nil {
		return "ok", ""
	}

	note = err.Error()

	switch {
	case telegram.IsFloodWait(err):
		secs := telegram.FloodWaitSeconds(err)
		s.log.Warn("⏳ FloodWait — menunggu sebelum retry",
			zap.Int64("group_id", g.GroupID),
			zap.Int("detik", secs),
		)
		// Sleep di dalam goroutine ini saja — tidak memblokir worker lain.
		select {
		case <-ctx.Done():
			return "flood_wait", "context cancelled during flood wait"
		case <-time.After(time.Duration(secs+5) * time.Second):
		}
		// Satu kali retry.
		if err2 := s.tg.SendMessage(ctx, g.GroupID, topicID, text, mediaPath); err2 == nil {
			return "ok", "retry after flood"
		} else {
			return "flood_wait", err2.Error()
		}

	case telegram.IsForbidden(err):
		s.log.Error("🚫 Forbidden — auto-blacklist",
			zap.Int64("group_id", g.GroupID), zap.String("error", note))
		if dbErr := db.SetGroupBlacklisted(s.sqlDB, g.GroupID, true); dbErr != nil {
			s.log.Error("Gagal simpan blacklist", zap.Error(dbErr))
		}
		return "forbidden", note

	default:
		s.log.Error("❌ Error pengiriman",
			zap.Int64("group_id", g.GroupID), zap.Error(err))
		return "error", note
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Random slot helper
// ─────────────────────────────────────────────────────────────────────────────

// randomSlots membagi window menjadi n slot yang sama besar dan memilih
// satu titik acak di dalam setiap slot menggunakan crypto/rand.
//
// Contoh: n=3, window=24h
//
//	Slot 0: 00:00–08:00 → misal pukul 02:47
//	Slot 1: 08:00–16:00 → misal pukul 11:23
//	Slot 2: 16:00–24:00 → misal pukul 19:51
//
// Hasilnya: pesan tersebar secara natural sepanjang hari tanpa clustering.
//
// Mengapa crypto/rand, bukan math/rand:
//   - math/rand sebelum Go 1.20 menggunakan global source yang tidak thread-safe.
//   - crypto/rand dijamin thread-safe dan menggunakan entropy OS (getrandom syscall).
func randomSlots(n int, total time.Duration, base time.Time) ([]time.Time, error) {
	if n <= 0 {
		return nil, nil
	}

	slotSize := total / time.Duration(n)
	times := make([]time.Time, n)

	for i := 0; i < n; i++ {
		start := time.Duration(i) * slotSize
		rangeNs := int64(slotSize)
		if rangeNs <= 0 {
			times[i] = base.Add(start)
			continue
		}
		offsetNs, err := rand.Int(rand.Reader, big.NewInt(rangeNs))
		if err != nil {
			return nil, fmt.Errorf("crypto/rand slot %d: %w", i, err)
		}
		times[i] = base.Add(start + time.Duration(offsetNs.Int64()))
	}

	return times, nil
}
