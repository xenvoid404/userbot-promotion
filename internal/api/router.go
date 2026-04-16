package api

import (
	"database/sql"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

// NewRouter membuat chi.Router dengan semua middleware dan route terdaftar.
//
// Struktur route:
//   - GET  /health            → tidak butuh auth (health check / monitoring)
//   - Semua route lain        → butuh Bearer token (BearerAuth middleware)
func NewRouter(sqlDB *sql.DB, reloadFn func()) http.Handler {
	h := NewHandler(sqlDB, reloadFn)

	r := chi.NewRouter()

	// ── Global middleware ──────────────────────────────────────────────────
	// Logger   : log setiap request ke stdout (format chi default)
	// Recoverer: catch panic → kembalikan 500, cegah crash seluruh server
	// RequestID: tambah X-Request-Id header untuk tracing
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	// CORS: izinkan semua origin — sesuaikan jika ingin membatasi domain dashboard.
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Authorization", "Content-Type"},
	}))

	// ── Public route ───────────────────────────────────────────────────────
	r.Get("/health", h.Health)

	// ── Protected routes ───────────────────────────────────────────────────
	r.Group(func(r chi.Router) {
		r.Use(BearerAuth(sqlDB))

		// Config
		r.Get("/config", h.ListConfig)
		r.Put("/config", h.SetConfig)

		// Messages
		r.Get("/messages", h.ListMessages)
		r.Post("/messages", h.CreateMessage)
		r.Get("/messages/{id}", h.GetMessage)
		r.Put("/messages/{id}", h.UpdateMessage)
		r.Delete("/messages/{id}", h.DeleteMessage)

		// Groups
		r.Get("/groups", h.ListGroups)
		r.Post("/groups", h.UpsertGroup)
		r.Get("/groups/{id}", h.GetGroup)
		r.Put("/groups/{id}", h.UpdateGroup)
		r.Delete("/groups/{id}", h.DeleteGroup)
		r.Post("/groups/{id}/blacklist", h.BlacklistGroup)
		r.Get("/groups/{id}/messages", h.GetGroupMessages)
		r.Put("/groups/{id}/messages", h.SetGroupMessages)

		// Rate limit
		r.Get("/ratelimit", h.GetRateLimit)
		r.Delete("/ratelimit/{id}", h.ResetRateLimit)

		// Logs & stats
		r.Get("/logs", h.ListLogs)
		r.Get("/logs/stats", h.GetStats)
	})

	return r
}
