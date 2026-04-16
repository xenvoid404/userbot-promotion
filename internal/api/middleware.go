package api

import (
	"crypto/subtle"
	"database/sql"
	"net/http"
	"strings"

	"github.com/xenvoid404/userbot-promotion/internal/db"
)

// BearerAuth adalah middleware autentikasi yang memvalidasi token Bearer dari
// header "Authorization: Bearer <token>".
//
// Token dibandingkan dengan subtle.ConstantTimeCompare untuk mencegah
// timing side-channel attack — perbandingan string biasa (==) dapat bocorkan
// informasi tentang panjang secret karena short-circuit saat menemukan
// karakter berbeda.
//
// Perilaku khusus:
//   - Jika api_secret belum dikonfigurasi atau masih "changeme", semua request
//     diizinkan untuk memudahkan setup awal. Segera ganti setelah pertama login!
func BearerAuth(sqlDB *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			secret, _ := db.GetConfig(sqlDB, db.KeyAPISecret)
			if secret == "" || secret == "changeme" {
				// Secret belum dikonfigurasi — izinkan akses untuk setup awal.
				next.ServeHTTP(w, r)
				return
			}

			authHeader := r.Header.Get("Authorization")
			token := strings.TrimPrefix(authHeader, "Bearer ")

			// Jika token == authHeader, berarti prefix "Bearer " tidak ada
			// → format header salah atau header tidak disertakan sama sekali.
			if token == authHeader {
				Unauthorized(w)
				return
			}

			if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
				Unauthorized(w)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
