// Command server runs the captain dashboard backend: chi HTTP server on
// RLS_PORT with SQLite persistence, cookie-based JWT auth and graceful
// shutdown on SIGINT/SIGTERM.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/rls-legacy/captain/backend/internal/api"
	"github.com/rls-legacy/captain/backend/internal/ingest"
	"github.com/rls-legacy/captain/backend/internal/notify"
	"github.com/rls-legacy/captain/backend/internal/sse"
	"github.com/rls-legacy/captain/backend/internal/store"
)

// Config is the runtime configuration resolved from environment variables.
type Config struct {
	Port              string
	DBPath            string
	JWTSecret         []byte
	RetentionDays     int64         // RLS_RETENTION_DAYS: retention window for REQ-040 pruning
	PruneTickSecond   int64         // RLS_PRUNE_TICK_SEC: retention sweep cadence (REQ-040)
	OfflineTickSecond int64         // RLS_OFFLINE_TICK_SEC: offline sweep cadence (REQ-015)
	Notifier          notify.Config // RLS_NOTIF_*: outbound alert channels (REQ-050)
}

// configFromEnv resolves env vars with the documented defaults.
func configFromEnv() (*Config, error) {
	port, ok := os.LookupEnv("RLS_PORT")
	if !ok || port == "" {
		port = "8080"
	}

	dbPath, ok := os.LookupEnv("RLS_DB_PATH")
	if !ok || dbPath == "" {
		dbPath = "./data/rls.db"
	}

	var secret []byte
	envSecret, ok := os.LookupEnv("RLS_JWT_SECRET")
	if !ok || envSecret == "" {
		// No secret configured: generate an ephemeral one in memory. Sessions
		// will not survive a restart — warn the operator.
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("generate jwt secret: %w", err)
		}
		secret = b
		slog.Warn("no RLS_JWT_SECRET: sesiones se invalidan al reiniciar")
	} else {
		secret = []byte(envSecret)
	}

	// REQ-040: retention default is 7 days (aligned with the compose file);
	// configurable via RLS_RETENTION_DAYS.
	retention := int64(7)
	if v, ok := os.LookupEnv("RLS_RETENTION_DAYS"); ok && v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid RLS_RETENTION_DAYS %q", v)
		}
		retention = n
	}

	offlineTick := int64(10)
	if v, ok := os.LookupEnv("RLS_OFFLINE_TICK_SEC"); ok && v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid RLS_OFFLINE_TICK_SEC %q", v)
		}
		offlineTick = n
	}

	// REQ-040: retention sweeps default to hourly; RLS_PRUNE_TICK_SEC only
	// exists so tests can shorten the cadence.
	pruneTick := int64(3600)
	if v, ok := os.LookupEnv("RLS_PRUNE_TICK_SEC"); ok && v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid RLS_PRUNE_TICK_SEC %q", v)
		}
		pruneTick = n
	}

	// REQ-050: notification channels (Discord webhook and/or Telegram bot).
	// All optional; both empty → inert notifier (no worker goroutine).
	// RLS_PUBLIC_URL is the public dashboard base URL; when set, alert
	// notifications append a deep link to the agent detail view.
	notifCfg := notify.Config{
		DiscordWebhookURL: os.Getenv("RLS_NOTIF_DISCORD_WEBHOOK_URL"),
		TelegramBotToken:  os.Getenv("RLS_NOTIF_TELEGRAM_BOT_TOKEN"),
		TelegramChatID:    os.Getenv("RLS_NOTIF_TELEGRAM_CHAT_ID"),
		PublicURL:         os.Getenv("RLS_PUBLIC_URL"),
	}
	if v, ok := os.LookupEnv("RLS_NOTIF_QUEUE_SIZE"); ok && v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid RLS_NOTIF_QUEUE_SIZE %q", v)
		}
		notifCfg.QueueSize = n
	}
	if v, ok := os.LookupEnv("RLS_NOTIF_TIMEOUT_SEC"); ok && v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid RLS_NOTIF_TIMEOUT_SEC %q", v)
		}
		notifCfg.TimeoutSec = n
	}

	return &Config{
		Port:              port,
		DBPath:            dbPath,
		JWTSecret:         secret,
		RetentionDays:     retention,
		PruneTickSecond:   pruneTick,
		OfflineTickSecond: offlineTick,
		Notifier:          notifCfg,
	}, nil
}

func main() {
	cfg, err := configFromEnv()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	// Ensure the DB directory exists (e.g. ./data).
	if dir := filepath.Dir(cfg.DBPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			slog.Error("create data directory", "dir", dir, "error", err)
			os.Exit(1)
		}
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("open store", "path", cfg.DBPath, "error", err)
		os.Exit(1)
	}
	defer st.Close()

	// SSE fan-out (TASK-004): a single broker instance is shared by the
	// heartbeat path (api) and the offline ticker (ingest), so every
	// state transition reaches subscribed dashboards.
	broker := sse.New()

	// REQ-050: outbound notifier (Discord/Telegram). Two credential sources,
	// resolved on every send: the notify_settings SQLite row (admin UI,
	// DB-first) with the RLS_NOTIF_* env vars as per-field fallback. Queue
	// size / timeout / retry ladder remain process runtime knobs (env).
	// Injected — variadic optional, like the broker — into the API server
	// (threshold alerts, resolves, test endpoint) and the offline ticker
	// (offline alerts).
	notifier := notify.NewDynamic(cfg.Notifier, func(ctx context.Context) (notify.Settings, error) {
		row, err := st.GetNotifySettings(ctx)
		if err != nil {
			return notify.Settings{}, err
		}
		return notify.Settings{
			DiscordWebhookURL: row.DiscordWebhookURL,
			TelegramBotToken:  row.TelegramBotToken,
			TelegramChatID:    row.TelegramChatID,
		}, nil
	})
	defer notifier.Stop(5 * time.Second)

	// cfg.Notifier travels as the env fallback so the GET settings view can
	// mask "what would actually send" (DB row first, env after).
	srv := api.New(st, cfg.JWTSecret, broker, notifier, cfg.Notifier)

	// Offline ticker (REQ-015/REQ-041): sweeps agents too quiet for
	// 3 × their interval, marks them offline and fires the alert. It stops
	// with the shutdown context and is awaited synchronously below.
	ticker := ingest.NewTicker(st, time.Duration(cfg.OfflineTickSecond)*time.Second, broker, notifier)
	tickerDone := make(chan struct{})

	// Retention pruner (REQ-040, TASK-008): hourly sweep that drops metric
	// samples and resolved alerts older than RLS_RETENTION_DAYS so the
	// SQLite volume stays sized (DEC-006). Same shutdown contract.
	pruner := ingest.NewPruner(st, time.Duration(cfg.PruneTickSecond)*time.Second, cfg.RetentionDays)
	prunerDone := make(chan struct{})

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown: listen for SIGINT/SIGTERM.
	notifyCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		ticker.Run(notifyCtx)
		close(tickerDone)
	}()

	go func() {
		pruner.Run(notifyCtx)
		close(prunerDone)
	}()

	go func() {
		slog.Info("captain listening", "port", cfg.Port, "db", cfg.DBPath)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-notifyCtx.Done()
	slog.Info("shutdown signal received, draining")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	}
	// Ticker exits on context cancellation; await it so no sweep races the
	// DB close (synchronous shutdown with the process).
	<-tickerDone
	<-prunerDone
	slog.Info("bye")
}
