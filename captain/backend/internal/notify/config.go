// config.go: dynamic (DB-first) credential resolution for the notifier.
// The admin can configure channels from the dashboard (notify_settings row
// in SQLite); the env vars RLS_NOTIF_* remain the process-level fallback.
// Resolution happens on EVERY send (fire, resolved and admin live test), so
// UI changes take effect immediately without restarting the backend.
package notify

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
)

// Settings is one snapshot of the persisted channel credentials (the
// notify_settings row, both channels + chat id). Empty field = the channel
// was never configured / was disabled from the UI (the field then falls
// back to its env var).
type Settings struct {
	DiscordWebhookURL string
	TelegramBotToken  string
	TelegramChatID    string
}

// Resolver reads the persisted settings (store-backed closure; the notify
// package never imports the store package itself). Returning an error makes
// the notifier degrade to the env config for that delivery.
type Resolver func(ctx context.Context) (Settings, error)

// NewDynamic is New plus a DB-backed resolver: the effective credentials are
// resolved per send as "DB row field set → wins; otherwise env fallback"
// (same field order on both sides). Queue/retry/timeout runtime knobs keep
// coming from the RLS_NOTIF_QUEUE_SIZE / RLS_NOTIF_TIMEOUT_SEC environment.
// A resolver is enough to spawn the worker even when the env leaves every
// channel empty: UI saves take effect on the next delivery.
func NewDynamic(cfg Config, resolve Resolver, clients ...*http.Client) *Notifier {
	return newNotifier(cfg, resolve, clients...)
}

// current returns the effective targets for one delivery attempt:
//
//	(discordEnabled, discordWebhookURL, telegramEnabled, telegramChatID, telegramEndpointURL)
//
// Static mode (no resolver): the New-time env config untouched. Dynamic
// mode: DB row fields first (trimmed), per-field env fallback; on resolver
// error it degrades to the full env config (warned once).
func (n *Notifier) current(ctx context.Context) (bool, string, bool, string, string) {
	dOn, tgOn := n.discordEnabled, n.telegramEnabled
	webhook, chat, tgURL := n.cfg.DiscordWebhookURL, n.cfg.TelegramChatID, n.telegramURL
	if n.resolve == nil {
		return dOn, webhook, tgOn, chat, tgURL
	}
	s, err := n.resolve(ctx)
	if err != nil {
		n.resolverWarnOnce.Do(func() {
			slog.Error("notify settings resolver failed: falling back to env config",
				"error", err)
		})
		return dOn, webhook, tgOn, chat, tgURL
	}

	// Per-field precedence: DB value first; the env var fills in whatever
	// the DB left empty (RLS_NOTIF_DISCORD_WEBHOOK_URL / RLS_NOTIF_TELEGRAM_*).
	tokenEff := strings.TrimSpace(s.TelegramBotToken)
	if tokenEff == "" {
		tokenEff = strings.TrimSpace(n.cfg.TelegramBotToken)
	}
	chatEff := strings.TrimSpace(s.TelegramChatID)
	if chatEff == "" {
		chatEff = strings.TrimSpace(n.cfg.TelegramChatID)
	}
	webhookEff := strings.TrimSpace(s.DiscordWebhookURL)
	if webhookEff == "" {
		webhookEff = strings.TrimSpace(n.cfg.DiscordWebhookURL)
	}

	dOn = webhookEff != ""
	tgOn = tokenEff != "" && chatEff != ""
	if tgOn {
		tgURL = telegramEndpoint(tokenEff)
	}
	return dOn, webhookEff, tgOn, chatEff, tgURL
}
