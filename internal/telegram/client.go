// Package telegram menyediakan wrapper tipis di atas gotd/td untuk kebutuhan userbot.
package telegram

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

// ─────────────────────────────────────────────────────────────────────────────
// Public types
// ─────────────────────────────────────────────────────────────────────────────

// Client adalah wrapper di atas gotd yang mengekspos operasi pengiriman
// pesan yang dibutuhkan scheduler.
type Client struct {
	raw   *tg.Client
	inner *telegram.Client
	log   *zap.Logger
}

// Config menyimpan kredensial dan konfigurasi koneksi Telegram.
type Config struct {
	APIID       int
	APIHash     string
	PhoneNumber string
	SessionFile string // path file sesi gotd (dibuat otomatis)
}

// ─────────────────────────────────────────────────────────────────────────────
// Error types
// ─────────────────────────────────────────────────────────────────────────────

// FloodWaitError dihasilkan saat Telegram mengembalikan FLOOD_WAIT_X.
type FloodWaitError struct {
	Seconds int
}

func (e *FloodWaitError) Error() string {
	return fmt.Sprintf("flood wait %d seconds", e.Seconds)
}

// IsFloodWait memeriksa apakah error adalah FloodWaitError.
func IsFloodWait(err error) bool {
	var fwe *FloodWaitError
	return errors.As(err, &fwe)
}

// FloodWaitSeconds mengekstrak durasi tunggu. Default 60 jika bukan FloodWaitError.
func FloodWaitSeconds(err error) int {
	var fwe *FloodWaitError
	if errors.As(err, &fwe) {
		return fwe.Seconds
	}
	return 60
}

// forbiddenCodes adalah kode error Telegram yang menandakan bot tidak diizinkan
// mengirim ke grup tersebut secara permanen. Grup yang menghasilkan salah satu
// error ini akan di-blacklist otomatis oleh scheduler.
var forbiddenCodes = []string{
	"CHAT_WRITE_FORBIDDEN",
	"USER_BANNED_IN_CHANNEL",
	"CHANNEL_PRIVATE",
	"CHAT_ADMIN_REQUIRED",
	"USER_NOT_PARTICIPANT",
}

// IsForbidden memeriksa apakah error menandakan larangan permanen.
func IsForbidden(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, code := range forbiddenCodes {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Run
// ─────────────────────────────────────────────────────────────────────────────

// Run menginisialisasi Telegram client, melakukan autentikasi jika sesi belum ada,
// lalu memanggil onReady dengan instance Client yang siap digunakan.
//
// Run bersifat blocking — jalankan dalam goroutine tersendiri.
// Akan return ketika ctx dibatalkan atau terjadi error fatal.
func Run(ctx context.Context, cfg Config, log *zap.Logger, onReady func(c *Client)) error {
	inner := telegram.NewClient(cfg.APIID, cfg.APIHash, telegram.Options{
		SessionStorage: &telegram.FileSessionStorage{Path: cfg.SessionFile},
		Logger:         log,
	})

	return inner.Run(ctx, func(ctx context.Context) error {
		status, err := inner.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}

		if !status.Authorized {
			log.Info("🔑 Sesi belum ada, memulai autentikasi interaktif...")
			if err := runInteractiveAuth(ctx, inner, cfg.PhoneNumber); err != nil {
				return err
			}
			log.Info("✅ Login berhasil", zap.String("session_file", cfg.SessionFile))
		}

		c := &Client{
			raw:   inner.API(),
			inner: inner,
			log:   log,
		}

		if onReady != nil {
			onReady(c)
		}

		<-ctx.Done()
		return nil
	})
}

// runInteractiveAuth menjalankan alur autentikasi OTP via stdin.
// Menggunakan bufio.Reader agar input dengan spasi terbaca dengan benar
// (berbeda dengan fmt.Scanln yang berhenti di spasi pertama).
func runInteractiveAuth(ctx context.Context, inner *telegram.Client, phone string) error {
	reader := bufio.NewReader(os.Stdin)
	flow := auth.NewFlow(
		auth.Constant(phone, "", auth.CodeAuthenticatorFunc(
			func(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
				fmt.Print("Masukkan kode OTP dari Telegram: ")
				code, err := reader.ReadString('\n')
				if err != nil {
					return "", fmt.Errorf("baca OTP: %w", err)
				}
				return strings.TrimSpace(code), nil
			},
		)),
		auth.SendCodeOptions{},
	)
	if err := inner.Auth().IfNecessary(ctx, flow); err != nil {
		return fmt.Errorf("auth flow: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Send & Upload
// ─────────────────────────────────────────────────────────────────────────────

// SendMessage mengirim pesan teks atau media ke grup.
// topicID = 0 berarti chat utama (bukan topik supergrup).
func (c *Client) SendMessage(
	ctx context.Context, groupID, topicID int64, text, mediaPath string,
) error {
	target := c.resolvePeer(groupID)

	// Menggunakan message.Sender dari gotd.
	// Mengapa kita menggunakan builder ini:
	// 1. Otomatis mengurus pembuatan RandomID yang thread-safe menggunakan crypto/rand (OS entropy),
	//    sehingga kita tidak perlu membuat fungsi PRNG manual yang rawan data race atau duplikasi.
	// 2. Sangat mudah menangani balasan (Reply) untuk sistem Topik/Thread.
	sender := message.NewSender(c.raw)

	var builder *message.RequestBuilder
	if topicID > 0 {
		builder = sender.To(target).ReplyMsgID(int(topicID))
	} else {
		builder = sender.To(target)
	}

	var err error
	if mediaPath != "" {
		// Menggunakan package uploader bawaan resmi dari gotd
		u := uploader.NewUploader(c.raw)
		f, errFile := os.Open(mediaPath)
		if errFile != nil {
			return fmt.Errorf("buka media: %w", errFile)
		}
		defer f.Close()

		stat, errStat := f.Stat()
		if errStat != nil {
			return fmt.Errorf("stat media: %w", errStat)
		}

		uploaded, errUpload := u.FromReader(ctx, stat.Name(), f)
		if errUpload != nil {
			return fmt.Errorf("upload media: %w", errUpload)
		}

		// Kirim media yang sudah diupload beserta caption-nya
		_, err = builder.Media(ctx, message.UploadedDocument(uploaded).Caption(text))
	} else {
		// Kirim pesan teks biasa
		_, err = builder.Text(ctx, text)
	}

	return wrapTGError(err)
}

// ─────────────────────────────────────────────────────────────────────────────
// Peer resolution
// ─────────────────────────────────────────────────────────────────────────────

// resolvePeer merakit InputPeer berdasarkan groupID yang didapat dari database.
//
// Telegram Bot API menggunakan format ID negatif (-100XXXXXXXXXX) untuk supergroup/channel,
// namun MTProto mewajibkan ID dalam bentuk positif murni. Normalisasi dilakukan di sini.
//
// CATATAN: Karena kita hanya menyimpan `group_id` di database tanpa `access_hash`,
// kita terpaksa menggunakan AccessHash: 0. Ini umumnya berfungsi dengan baik pada Userbot
// karena server Telegram akan me-resolve-nya melalui cache internal dialog yang ada.
func (c *Client) resolvePeer(groupID int64) tg.InputPeerClass {
	absID := groupID
	if absID < 0 {
		absID = -absID
		// Strip prefiks 100 dari ID supergroup/channel format Bot API.
		if absID > 1_000_000_000 {
			absID -= 1_000_000_000
		}
		return &tg.InputPeerChannel{ChannelID: absID, AccessHash: 0}
	}
	return &tg.InputPeerChat{ChatID: absID}
}

// ─────────────────────────────────────────────────────────────────────────────
// Error wrapping
// ─────────────────────────────────────────────────────────────────────────────

// wrapTGError mengkonversi error MTProto menjadi tipe Go yang lebih spesifik.
//
// Parsing FLOOD_WAIT menggunakan strings.Cut + strconv.Atoi, bukan fmt.Sscanf,
// karena Sscanf dapat gagal secara silent jika format string dari Telegram berubah di masa depan.
func wrapTGError(err error) error {
	if err == nil {
		return nil
	}

	const prefix = "FLOOD_WAIT_"
	msg := err.Error()
	idx := strings.Index(msg, prefix)
	if idx < 0 {
		return err
	}

	// Ekstrak bagian numerik setelah "FLOOD_WAIT_"
	rest := msg[idx+len(prefix):]
	numStr, _, _ := strings.Cut(rest, " ")
	numStr, _, _ = strings.Cut(numStr, ":")
	numStr = strings.TrimSpace(numStr)

	if secs, parseErr := strconv.Atoi(numStr); parseErr == nil && secs > 0 {
		return &FloodWaitError{Seconds: secs}
	}
	// Gagal parse angka — pakai estimasi aman 60 detik
	return &FloodWaitError{Seconds: 60}
}
