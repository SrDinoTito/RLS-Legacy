// Package api wires the chi router: middleware, routes and HTTP handlers.
package api

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/rls-legacy/captain/backend/internal/notify"
	"github.com/rls-legacy/captain/backend/internal/sse"
	"github.com/rls-legacy/captain/backend/internal/store"
)

// SessionCookie is the name of the httpOnly JWT session cookie.
const SessionCookie = "rls_session"

// sessionTTL is the lifetime of a session JWT and its cookie.
const sessionTTL = 24 * time.Hour

// Server holds shared dependencies for all handlers.
type Server struct {
	store        *store.Store
	jwtSecret    []byte
	now          func() time.Time // injectable clock (tests)
	broker       *sse.Broker      // SSE fan-out (REQ-018); never nil
	notifier     *notify.Notifier // outbound alert notifications (REQ-050); nil-safe
	notifEnv     notify.Config    // RLS_NOTIF_* env fallback (GET settings masking); zero = none
	downloadDir  string           // allowlisted Stowaway artifacts bundled in the Captain image
	dashboardURL string           // required RLS_PUBLIC_URL used by installer instructions
}

// PublicConfig contains non-secret runtime settings safe to expose to the UI.
type PublicConfig struct {
	DashboardURL string
}

// New builds a Server bound to the given store and signing secret. Extras
// (variadic optional, like the original broker argument) are matched by
// type: *sse.Broker replaces the internal SSE broker; *notify.Notifier
// enables the outbound alert notifications (REQ-050); notify.Config is the
// env fallback credentials for the notify settings GET (masking view).
// Omitted extras fall back to safe defaults (fresh broker, nil notifier,
// no env fallback).
func New(st *store.Store, jwtSecret []byte, extras ...any) *Server {
	b := sse.New()
	var n *notify.Notifier
	var envCfg notify.Config
	var publicCfg PublicConfig
	for _, x := range extras {
		switch one := x.(type) {
		case *sse.Broker:
			if one != nil {
				b = one
			}
		case *notify.Notifier:
			if one != nil {
				n = one
			}
		case notify.Config:
			envCfg = one
		case PublicConfig:
			publicCfg = one
		}
	}
	downloadDir := os.Getenv("RLS_BRIDGE_DOWNLOAD_DIR")
	if downloadDir == "" {
		downloadDir = "/srv/downloads"
	}
	return &Server{store: st, jwtSecret: jwtSecret, now: time.Now, broker: b, notifier: n, notifEnv: envCfg, downloadDir: downloadDir, dashboardURL: strings.TrimRight(strings.TrimSpace(publicCfg.DashboardURL), "/")}
}

// Handler returns the fully wired chi router (middleware included).
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	// Public endpoints: no auth.
	r.Get("/healthz", s.handleHealthz)
	r.Get("/api/v1/public-config", s.handlePublicConfig)
	r.With(s.requireVPSInstallToken).Get("/script.sh", s.handleBridgeInstallScript)
	r.With(s.requireVPSInstallToken).Get("/downloads/rls-stowaway-linux/{asset}", s.handleBridgeDownload)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/auth/bootstrap/status", s.handleBootstrapStatus)
		r.Post("/auth/bootstrap", s.handleBootstrap)
		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/logout", s.handleLogout)

		// Agent ingest endpoints: Bearer token auth (NOT session cookies).
		r.Group(func(r chi.Router) {
			r.Use(s.requireAgentToken)
			r.Post("/agents/heartbeat", s.handleHeartbeat)
		})

		// Authenticated endpoints (valid cookie JWT required).
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/auth/me", s.handleMe)

			// Agents + metrics + alerts + SSE stream (viewable by any
			// authenticated dashboard user).
			r.Get("/agents", s.handleListAgents)
			r.Get("/agents/{id}/history", s.handleAgentHistory)
			r.Get("/agents/{id}/alerts", s.handleAgentAlerts)
			r.Get("/agents/{id}/thresholds", s.handleGetThresholds)
			r.Put("/agents/{id}/thresholds", s.handlePutThresholds)
			r.Get("/alerts", s.handleListAlerts)
			r.Post("/alerts/{id}/resolve", s.handleResolveAlert)
			r.Get("/stream", s.handleStream)

			// Change-password: admin changes anyone, a user may change their
			// own (self case) → handler enforces admin-or-self, not the group.
			r.Put("/users/{id}/password", s.handleChangeUserPassword)

			// Admin-only endpoints.
			r.Group(func(r chi.Router) {
				r.Use(s.requireAdmin)
				r.Get("/users", s.handleListUsers)
				r.Post("/users", s.handleCreateUser)
				r.Delete("/users/{id}", s.handleDeleteUser)

				// Token lifecycle CRUD (admin): DEC-011 keeps the plaintext
				// persisted for always-visible UI copy; revoke auto-dismisses
				// the linked agent. tags is the shared ecosystem tag set.
				r.Get("/tokens", s.handleListTokens)
				r.Post("/tokens", s.handleCreateToken)
				r.Put("/tokens/{id}/tags", s.handlePutTokenTags)
				r.Delete("/tokens/{id}", s.handleRevokeToken)

				// Agent lifecycle (admin, TASK-009): DELETE is a SOFT
				// dismiss — the row is kept as a permanent record
				// (metrics/history/resolved alerts), only hidden from the
				// active views. Heartbeats from the same token restore it.
				r.Delete("/agents/{id}", s.handleDeleteAgent)
				r.Put("/agents/{id}/monitoring", s.handlePutAgentMonitoring)
				r.Put("/agents/{id}/container-tags", s.handlePutAgentContainerTags)
				r.Get("/notification-policies/{scope}/{scopeID}", s.handleGetNotificationPolicy)
				r.Put("/notification-policies/{scope}/{scopeID}", s.handlePutNotificationPolicy)
				r.Delete("/notification-policies/{scope}/{scopeID}", s.handleDeleteNotificationPolicy)

				// Notification channel test (REQ-050): admin pings a
				// configured Discord/Telegram channel without waiting for
				// a real alert.
				r.Post("/notify/test", s.handleNotifyTest)

				// Notification channel settings (REQ-050, dashboard-driven):
				// GET returns masked values (never plaintext), PUT validates
				// and upserts the SQLite row (the notifier picks it up live).
				r.Get("/notify/settings", s.handleGetNotifySettings)
				r.Put("/notify/settings", s.handlePutNotifySettings)
			})
		})
	})
	return r
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
