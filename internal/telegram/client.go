// Package telegram menyediakan wrapper tipis di atas gotd/td untuk kebutuhan userbot.
package telegram

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

// ─────────────────────────────────────────────────────────────────────────────
// Public types
// ─────────────────────────────────────────────────────────────────────────────

// Client adalah wrapper di atas gotd yang mengekspos operasi pengiriman
// pesan yang dibutuhkan scheduler.
type Client struct {
	raw     *tg.Client
	inner   *telegram.Client
	manager *peers.Manager
	log     *zap.Logger
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
			log.Info("✅ Login berhasil",
				zap.String("session_file", cfg.SessionFile))
		}

		raw := inner.API()

		// peers.Manager adalah resolver resmi gotd.
		// Dia meng-cache access_hash secara otomatis dari update/message history.
		// Ini menggantikan pendekatan lama (access_hash=0) yang menghasilkan
		// CHANNEL_INVALID / PEER_ID_INVALID secara intermittent.
		manager := peers.Options{}.Build(raw)

		c := &Client{
			raw:     raw,
			inner:   inner,
			manager: manager,
			log:     log,
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
// Send
// ─────────────────────────────────────────────────────────────────────────────

// SendMessage mengirim pesan teks atau media ke grup.
// topicID = 0 berarti chat utama (bukan topik supergrup).
func (c *Client) SendMessage(
	ctx context.Context, groupID, topicID int64, text, mediaPath string,
) error {
	peer, err := c.resolvePeer(ctx, groupID)
	if err != nil {
		return fmt.Errorf("resolve peer %d: %w", groupID, err)
	}

	if mediaPath != "" {
		return c.sendMedia(ctx, peer, topicID, text, mediaPath)
	}
	return c.sendText(ctx, peer, topicID, text)
}

func (c *Client) sendText(
	ctx context.Context, peer tg.InputPeerClass, topicID int64, text string,
) error {
	id, err := cryptoRandID()
	if err != nil {
		return fmt.Errorf("generate random id: %w", err)
	}

	req := &tg.MessagesSendMessageRequest{
		Peer:     peer,
		Message:  text,
		RandomID: id,
	}
	if topicID > 0 {
		req.ReplyTo = &tg.InputReplyToMessage{ReplyToMsgID: int(topicID)}
	}

	_, err = c.raw.MessagesSendMessage(ctx, req)
	return wrapTGError(err)
}

func (c *Client) sendMedia(
	ctx context.Context, peer tg.InputPeerClass, topicID int64, caption, filePath string,
) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("buka media: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat media: %w", err)
	}

	uploaded, err := c.inner.Upload().FromReader(ctx, stat.Name(), f)
	if err != nil {
		return fmt.Errorf("upload media: %w", err)
	}

	id, err := cryptoRandID()
	if err != nil {
		return fmt.Errorf("generate random id: %w", err)
	}

	req := &tg.MessagesSendMediaRequest{
		Peer:     peer,
		Media:    &tg.InputMediaUploadedDocument{File: uploaded},
		Message:  caption,
		RandomID: id,
	}
	if topicID > 0 {
		req.ReplyTo = &tg.InputReplyToMessage{ReplyToMsgID: int(topicID)}
	}

	_, err = c.raw.MessagesSendMedia(ctx, req)
	return wrapTGError(err)
}

// ─────────────────────────────────────────────────────────────────────────────
// Peer resolution
// ─────────────────────────────────────────────────────────────────────────────

// resolvePeer menggunakan peers.Manager untuk mendapatkan InputPeer yang valid.
//
// Telegram Bot API menggunakan -100XXXXXXXXXX untuk channel/supergroup,
// namun MTProto memakai ID positif. Normalisasi dilakukan di sini.
// peers.Manager menyimpan access_hash secara otomatis dari update/history —
// tidak perlu menyimpan hash secara manual, dan tidak ada hack access_hash=0.
func (c *Client) resolvePeer(ctx context.Context, groupID int64) (tg.InputPeerClass, error) {
	absID := groupID
	if absID < 0 {
		absID = -absID
		// Strip prefiks 100 dari ID supergroup/channel format Bot API.
		if absID > 1_000_000_000 {
			absID -= 1_000_000_000
		}
	}

	// Coba sebagai channel/supergroup terlebih dahulu.
	if ch, err := c.manager.Channel(ctx, &tg.InputChannel{ChannelID: absID}); err == nil {
		return ch.InputPeer(), nil
	}

	// Fallback ke group chat biasa.
	if chat, err := c.manager.Chat(ctx, absID); err == nil {
		return chat.InputPeer(), nil
	}

	return nil, fmt.Errorf("tidak bisa resolve peer untuk group_id=%d", groupID)
}

// ─────────────────────────────────────────────────────────────────────────────
// Error wrapping
// ─────────────────────────────────────────────────────────────────────────────

// wrapTGError mengkonversi error MTProto menjadi tipe Go yang lebih spesifik.
//
// Parsing FLOOD_WAIT menggunakan strings.Cut + strconv.Atoi, bukan fmt.Sscanf,
// karena Sscanf dapat gagal secara silent jika format string berubah.
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
	// Gagal parse angka — pakai estimasi 60 detik
	return &FloodWaitError{Seconds: 60}
}

// ─────────────────────────────────────────────────────────────────────────────
// Crypto helper
// ─────────────────────────────────────────────────────────────────────────────

// cryptoRandID menghasilkan random ID 64-bit menggunakan crypto/rand (OS entropy).
//
// Mengapa tidak pakai PRNG global (math/rand / xorshift):
//  1. PRNG global tidak thread-safe sebelum Go 1.20 → data race di multi-goroutine.
//  2. PRNG deterministik berisiko menghasilkan ID duplikat setelah restart →
//     Telegram menolak pesan sebagai duplikat.
//  3. crypto/rand dijamin thread-safe dan menggunakan getrandom / /dev/urandom.
func cryptoRandID() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("crypto/rand: %w", err)
	}
	return int64(binary.LittleEndian.Uint64(b[:])), nil
}
