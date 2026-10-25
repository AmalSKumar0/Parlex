package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"parlex/internal/auth"
	"parlex/internal/config"
)

// NewRouter sets up the Chi HTTP router with all middleware and route handlers.
func NewRouter(
	cfg *config.Config,
	h *Handlers,
	tm *auth.TokenManager,
	rev *auth.RevocationRegistry,
	streamHandler http.HandlerFunc,
	logger *slog.Logger,
) http.Handler {
	r := chi.NewRouter()

	// Base middleware stack
	r.Use(RequestIDMiddleware)
	r.Use(RecoveryMiddleware(logger))
	r.Use(CORSMiddleware(cfg.CORSAllowedOrigins))
	r.Use(LoggingMiddleware(logger))
	r.Use(BodyLimitMiddleware)

	// Health endpoints
	r.Get("/health/live", h.HealthLive)
	r.Get("/health/ready", h.HealthReady)

	// API v1 routes
	r.Route("/v1", func(r chi.Router) {
		r.Get("/languages", h.GetLanguages)
		r.Post("/devices/claim", h.ClaimDevice)
		r.Post("/auth/device-token", h.IssueDeviceToken)
		r.Get("/firmware/latest", h.GetLatestFirmware)

		// WebSocket stream endpoint
		if streamHandler != nil {
			r.HandleFunc("/stream", streamHandler)
		}

		// Admin routes protected by X-Admin-Key
		r.Group(func(r chi.Router) {
			r.Use(AdminAuth(cfg.AdminAPIKey))
			r.Post("/admin/devices", h.AdminCreateDevice)
			r.Post("/admin/devices/{id}/revoke", h.AdminRevokeDevice)
			r.Post("/admin/firmware", h.AdminRegisterFirmware)
		})

		// Device / Owner authenticated routes
		r.Group(func(r chi.Router) {
			r.Use(RequireToken(tm, rev, ""))
			r.Get("/devices/{id}/settings", h.GetDeviceSettings)
			r.Patch("/devices/{id}/settings", h.UpdateDeviceSettings)
			r.Get("/devices/{id}/status", h.GetDeviceStatus)
			r.Get("/devices/{id}/usage", h.GetDeviceUsage)
		})
	})

	// Static Demo UI routes
	fs := http.FileServer(http.Dir("demo"))
	r.Handle("/demo/*", http.StripPrefix("/demo", fs))
	r.Get("/demo", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/demo/", http.StatusPermanentRedirect)
	})

	return r
}
