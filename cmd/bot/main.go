// Command userbot-promotion adalah entry point untuk Telegram Userbot Promotion.
//
// Urutan startup:
//  1. Buka database SQLite (jalankan migration otomatis jika perlu)
//  2. Jalankan REST API server di goroutine terpisah
//  3. Jika config Telegram lengkap, jalankan Telegram client + scheduler
//  4. Tunggu sinyal OS (SIGINT / SIGTERM) untuk graceful shutdown
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/xenvoid404/userbot-promotion/internal/api"
	"github.com/xenvoid404/userbot-promotion/internal/db"
	"github.com/xenvoid404/userbot-promotion/internal/scheduler"
	"github.com/xenvoid404/userbot-promotion/internal/telegram"
	"go.uber.org/zap"
)

func main() {
	// ── Logger ────────────────────────────────────────────────────────────
	log, err := zap.NewProduction()
	if err != nil {
		panic("gagal inisialisasi logger: " + err.Error())
	}
	defer log.Sync() //nolint:errcheck

	// ── Database ──────────────────────────────────────────────────────────
	dbPath := envOr("DB_PATH", "sqlite.db")
	sqlDB, err := db.Open(dbPath)
	if err != nil {
		log.Fatal("Gagal buka database", zap.Error(err))
	}
	defer sqlDB.Close()
	log.Info("✅ Database terbuka", zap.String("path", dbPath))

	// ── Baca config Telegram dari DB ──────────────────────────────────────
	// Config ini dibaca sekali saat startup. Jika diubah via API, user perlu
	// restart service agar perubahan api_id/api_hash/phone_number berlaku.
	// Perubahan lain (limit, pesan, grup) berlaku langsung via reloadFn.
	apiIDStr, _ := db.GetConfig(sqlDB, db.KeyAPIID)
	apiHash, _ := db.GetConfig(sqlDB, db.KeyAPIHash)
	phone, _ := db.GetConfig(sqlDB, db.KeyPhoneNumber)
	apiID, _ := strconv.Atoi(apiIDStr)

	tgReady := apiID > 0 && apiHash != "" && phone != ""
	if !tgReady {
		log.Warn("⚠️  Konfigurasi Telegram belum lengkap — bot tidak akan berjalan")
		log.Warn("    Set via API: PUT /config dengan key api_id, api_hash, phone_number")
		log.Warn("    Lalu restart service agar perubahan berlaku")
	}

	tgCfg := telegram.Config{
		APIID:       apiID,
		APIHash:     apiHash,
		PhoneNumber: phone,
		SessionFile: envOr("SESSION_FILE", "session.json"),
	}

	// ── Root context dengan signal handling ───────────────────────────────
	rootCtx, rootCancel := signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM)
	defer rootCancel()

	// ── Scheduler holder (thread-safe) ────────────────────────────────────
	// schedMu melindungi pointer sched agar aman diakses dari:
	// - goroutine Telegram (assign saat onReady dipanggil)
	// - goroutine HTTP handler (baca saat reloadFn dipanggil)
	// - goroutine main (baca saat shutdown)
	var (
		schedMu sync.Mutex
		sched   *scheduler.Scheduler
	)

	// reloadFn dipanggil oleh HTTP handler setelah mutasi config/pesan/grup.
	// Scheduler di-restart agar jadwal baru langsung berlaku tanpa tunggu
	// siklus 24 jam berikutnya.
	reloadFn := func() {
		schedMu.Lock()
		s := sched
		schedMu.Unlock()

		if s == nil {
			return
		}
		log.Info("🔄 Reload scheduler — jadwal ulang siklus aktif")
		s.Stop()
		s.Start(rootCtx)
	}

	// ── WaitGroup untuk semua goroutine utama ─────────────────────────────
	var wg sync.WaitGroup

	// ── REST API ──────────────────────────────────────────────────────────
	apiPort, _ := db.GetConfig(sqlDB, db.KeyAPIPort)
	if apiPort == "" {
		apiPort = "8080"
	}

	server := &http.Server{
		Addr:         ":" + apiPort,
		Handler:      api.NewRouter(sqlDB, reloadFn),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Info("🌐 REST API berjalan", zap.String("port", apiPort))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("REST API error", zap.Error(err))
		}
	}()

	// ── Telegram Client + Scheduler ───────────────────────────────────────
	if tgReady {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := telegram.Run(rootCtx, tgCfg, log, func(client *telegram.Client) {
				log.Info("✅ Telegram client siap")

				s := scheduler.New(sqlDB, client, log)

				schedMu.Lock()
				sched = s
				schedMu.Unlock()

				s.Start(rootCtx)
			})
			if err != nil && rootCtx.Err() == nil {
				// Error yang bukan karena context dibatalkan = error fatal.
				log.Error("Telegram client berhenti dengan error", zap.Error(err))
			}
		}()
	}

	// ── Graceful shutdown ─────────────────────────────────────────────────
	<-rootCtx.Done()
	log.Info("🛑 Shutdown dimulai...")

	// 1. Stop scheduler terlebih dahulu agar tidak ada pengiriman baru
	//    saat HTTP server sedang shutdown.
	schedMu.Lock()
	s := sched
	schedMu.Unlock()
	if s != nil {
		s.Stop()
		log.Info("   Scheduler dihentikan")
	}

	// 2. Graceful shutdown HTTP server — beri waktu 10 detik untuk
	//    menyelesaikan request yang sedang in-flight.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	if err := server.Shutdown(shutCtx); err != nil {
		log.Error("HTTP server shutdown error", zap.Error(err))
	} else {
		log.Info("   HTTP server dihentikan")
	}

	// 3. Tunggu semua goroutine selesai.
	wg.Wait()
	log.Info("👋 Bye!")
}

// envOr mengembalikan nilai environment variable key, atau def jika tidak ada.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
