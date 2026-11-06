// Package notify implements the outbound notification channel (REQ-050):
// alert_new / alert_resolved events fan out to the configured incoming
// webhooks/bots (Discord and/or Telegram). It is deliberately decoupled from
// the ingest path: Enqueue never blocks, delivery runs in a single background
// worker with bounded retries, so an unreachable webhook can never slow
// heartbeats down. With no channels configured the notifier is fully inert
// (no worker goroutine at all).
//
// Event flow: symmetric with the SSE broker — the same emission sites
// (heartbeat thresholds, offline ticker, resolve endpoints) that broadcast
// alert_new / alert_resolved into the dashboard also notify out-of-band
// channels here. Heartbeats are never notified.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// notifiedTypes are the alert classes that generate notifications. Heartbeat
// traffic and any other event kind are silently dropped.
var notifiedTypes = map[string]bool{"offline": true, "high_cpu": true, "high_ram": true}

// Embed colors used by the Discord webhook: offline fires red, threshold
// (high_cpu / high_ram) fires orange, and every resolution renders green
// ("señal restablecida") so it stands out at a glance in the channel.
const (
	discordColorFired     = 15008433 // fired offline alerts (red)
	discordColorThreshold = 15105570 // fired high_cpu / high_ram (orange)
	discordColorResolved  = 3066993  // resolved alerts (green)
)

// Default queue depth and HTTP timeout for the notifier.
const (
	DefaultQueueSize  = 64 // RLS_NOTIF_QUEUE_SIZE
	DefaultTimeoutSec = 10 // RLS_NOTIF_TIMEOUT_SEC
)

// ErrNotConfigured is returned by SendTest when the requested channel has no
// webhook/token configured.
var ErrNotConfigured = errors.New("notification channel not configured")

// Kind identifies which side of the alert lifecycle the event belongs to.
type Kind string

const (
	KindNew      Kind = "new"
	KindResolved Kind = "resolved"
)

// Event is the payload shared between the alert-emission sites and the
// notifier. AgentName is ALWAYS set: the caller passes it from the broadcast
// site (it has the agent in context) or does a store lookup when only the
// agent_id is at hand.
//
// The enrichment fields (Tags..UptimeSec) are the notification context v2:
// the emission sites fill what they already hold in hand (the agent row and
// the metric sample that triggered the alert), and anything else degrades to
// omitted — a purged agent or a zero-metric event simply loses the optional
// lines, never the alert itself. The dashboard deep link is NOT transported
// per event: the notifier derives it from Config.PublicURL (RLS_PUBLIC_URL).
type Event struct {
	AgentID   int64
	AgentName string // display name (agent row, RLS_NAME or hostname)
	Type      string // offline | high_cpu | high_ram
	Value     float64
	Message   string // human wording (aligned with the alerts table message)
	TS        int64  // unix seconds: fired_at when Kind==new, resolved_at when resolved
	Kind      Kind   // new | resolved

	Tags        []string // ecosystem tags (JSON array on the row, nil = none)
	Hostname    string
	ContainerID string
	Interval    int // reported heartbeat cadence (seconds)

	LastCPU     *float64 // latest sample cpu_pct
	LastRamUsed *float64 // latest sample ram_used_mb
	LastRamLim  *float64 // latest sample ram_limit_mb
	UptimeSec   *int64   // latest sample uptime_sec (process uptime)
}

// Config mirrors the RLS_NOTIF_* environment variables. Empty/absent fields
// disable the associated channel; with no channel at all the notifier is
// inert (New returns one without a worker).
type Config struct {
	DiscordWebhookURL string // RLS_NOTIF_DISCORD_WEBHOOK_URL
	TelegramBotToken  string // RLS_NOTIF_TELEGRAM_BOT_TOKEN
	TelegramChatID    string // RLS_NOTIF_TELEGRAM_CHAT_ID
	PublicURL         string // RLS_PUBLIC_URL: dashboard base URL for the notification deep links ("" = no link)
	QueueSize         int64  // RLS_NOTIF_QUEUE_SIZE (default 64)
	TimeoutSec        int64  // RLS_NOTIF_TIMEOUT_SEC (default 10)
}

// dashboardLink returns the deep link to the agent detail view of the
// dashboard — {RLS_PUBLIC_URL}/agents/{id} — or "" when RLS_PUBLIC_URL is
// not set (then no link is appended to the messages).
func (c Config) dashboardLink(agentID int64) string {
	base := strings.TrimRight(strings.TrimSpace(c.PublicURL), "/")
	if base == "" || agentID <= 0 {
		return ""
	}
	return base + "/agents/" + strconv.FormatInt(agentID, 10)
}

// statusError is an HTTP-level failure. 5xx (and only 5xx) is retryable:
// 4xx means the webhook/chat_id is wrong and retrying cannot fix it.
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	if e.body != "" {
		return fmt.Sprintf("HTTP %d: %s", e.code, e.body)
	}
	return fmt.Sprintf("HTTP %d", e.code)
}

func (e *statusError) serverSide() bool { return e.code >= 500 }

// Notifier queues events and delivers them to every configured channel.
// Safe for concurrent use; Enqueue never blocks (full queue → drop + counter).
type Notifier struct {
	cfg    Config
	client *http.Client // per-attempt timeout comes from the client

	ch     chan Event // nil when inert (no channels configured)
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc

	backoff []time.Duration // retry delays 1s/2s/4s (tests may shrink it)

	dropped atomic.Int64 // counter of Enqueue drops (logged on every drop)

	discordEnabled  bool
	telegramEnabled bool

	discordWarnOnce  sync.Once // 4xx misconfiguration: warn ONCE, never retry
	telegramWarnOnce sync.Once

	resolve          Resolver  // DB-first dynamic credentials (nil = static env config)
	resolverWarnOnce sync.Once // resolver error: warn ONCE, env fallback
	telegramURL      string    // https://api.telegram.org/bot<TOKEN>/sendMessage (tests override)
}

// New builds a Notifier. Without at least one fully configured channel the
// returned notifier is inert: Enqueue is a no-op and no worker is spawned.
// An optional *http.Client overrides the default (tests).
func New(cfg Config, clients ...*http.Client) *Notifier {
	return newNotifier(cfg, nil, clients...)
}

// newNotifier is the shared constructor: static config (resolve==nil) keeps
// the exact env-driven behavior; a resolver switches to DB-first dynamic
// credential resolution (see config.go).
func newNotifier(cfg Config, resolve Resolver, clients ...*http.Client) *Notifier {
	queue, timeout := normalizedInts(&cfg)
	n := &Notifier{
		cfg:     cfg,
		resolve: resolve,
		done:    make(chan struct{}),
		backoff: []time.Duration{
			time.Second, 2 * time.Second, 4 * time.Second, // x3 backoff
		},
		discordEnabled: strings.TrimSpace(cfg.DiscordWebhookURL) != "",
		telegramEnabled: strings.TrimSpace(cfg.TelegramBotToken) != "" &&
			strings.TrimSpace(cfg.TelegramChatID) != "",
	}
	n.client = &http.Client{Timeout: time.Duration(timeout) * time.Second}
	for _, c := range clients {
		if c != nil {
			n.client = c
		}
	}
	if !n.discordEnabled && !n.telegramEnabled && n.resolve == nil {
		n.done = closedDone() // inert: Enqueue drops everything, Stop no-op
		return n
	}
	if n.telegramEnabled {
		n.telegramURL = telegramEndpoint(n.cfg.TelegramBotToken)
	}
	n.ch = make(chan Event, queue)
	n.ctx, n.cancel = context.WithCancel(context.Background())
	go n.worker()
	return n
}

func closedDone() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// normalizedInts applies QueueSize / TimeoutSec defaults in place and returns
// them (they are only used by the engine, so defaults do not matter when
// inert — but the config stays traceable).
func normalizedInts(cfg *Config) (queue, timeout int64) {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = DefaultQueueSize
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = DefaultTimeoutSec
	}
	return cfg.QueueSize, cfg.TimeoutSec
}

// Enqueue accepts an event without ever blocking; when the queue is full the
// event is dropped and the drop counter is logged. Only notify-worthy alert
// classes pass through (heartbeats never reach here, but defense-in-depth).
func (n *Notifier) Enqueue(evt Event) {
	if n.ch == nil || !notifiedTypes[evt.Type] {
		return
	}
	select {
	case n.ch <- evt:
	default:
		total := n.dropped.Add(1)
		slog.Warn("notifier queue full: event dropped",
			"dropped_total", total, "type", evt.Type, "agent_name", evt.AgentName)
	}
}

// worker consumes the queue until Stop closes it. Single dedicated worker by
// contract: deliveries retry on their own schedule, trading throughput for
// extreme-lightweight resource usage (DEC-006).
func (n *Notifier) worker() {
	defer close(n.done)
	for evt := range n.ch {
		n.deliver(evt)
	}
}

// deliver sends one event to every configured channel, each with its own
// bounded retry ladder (x3, backoff 1s/2s/4s on network errors or 5xx).
// Effective credentials are resolved per delivery (DB row first, env
// fallback) so dashboard saves take effect on the next event.
func (n *Notifier) deliver(evt Event) {
	dOn, webhook, tgOn, chat, tgURL := n.current(n.ctx)
	if dOn {
		n.sendWithRetry("discord", &n.discordWarnOnce, func(ctx context.Context) error {
			return n.postDiscord(ctx, evt, webhook)
		})
	}
	if tgOn {
		n.sendWithRetry("telegram", &n.telegramWarnOnce, func(ctx context.Context) error {
			return n.postTelegram(ctx, evt, chat, tgURL)
		})
	}
}

// sendWithRetry runs attempt() until success, permanent refusal (4xx: warn
// once, config is wrong) or exhaustion (log error and move on: notifications
// are best-effort by design and must not resurrect cron-style loops).
func (n *Notifier) sendWithRetry(channel string, warnOnce *sync.Once, attempt func(context.Context) error) {
	for i := 0; ; i++ {
		err := attempt(n.ctx)
		if err == nil {
			return
		}
		var se *statusError
		switch {
		case errors.As(err, &se) && !se.serverSide():
			// 4xx: bad webhook URL / wrong chat_id. Config is wrong, not
			// transient — never retry, log clearly exactly once per channel.
			warnOnce.Do(func() {
				slog.Error("notification channel misconfigured: not retrying",
					"channel", channel, "status", se.code, "detail", se.body,
					"hint", "revise el webhook/token+chat de "+channel)
			})
			return
		case n.ctx.Err() != nil:
			return // shutdown: stop trying
		case i >= len(n.backoff):
			slog.Error("notification delivery failed after retries",
				"channel", channel, "attempts", i+1, "error", err)
			return
		default:
			slog.Warn("notification delivery failed, retrying",
				"channel", channel, "attempt", i+1, "of", len(n.backoff), "error", err)
		}
		select {
		case <-time.After(n.backoff[i]):
		case <-n.ctx.Done():
			return
		}
	}
}

// ---- Telegram ------------------------------------------------------------

// firedPhrase is the short per-type phrase used in both Discord titles and
// Telegram headlines ("🛰 {name} — sin comunicación" / "CPU sobre umbral").
var firedPhrase = map[string]string{
	"offline":  "sin comunicación",
	"high_cpu": "CPU sobre umbral",
	"high_ram": "RAM sobre umbral",
}

// resolvedPhrase mirrors it on the resolution side.
func resolvedPhrase(typ string) string {
	if typ == "offline" {
		return "señal restablecida"
	}
	return "alerta resuelta"
}

// postTelegram sends the enriched HTML message for one event:
//
//	⚠️ <b>RLS · mi-nginx-prod</b> — sin comunicación
//	<i>sin comunicación (posible caída)</i>
//	🏷 lab, infra
//	🖥 web-1 · abc123def456
//	📊 CPU 12.4% · RAM 145.2/512 MB · ⏱ 2h 21m
//	generada 14:05 UTC
//	📊 <a href="https://...">Panel completo</a>
//
// Every enrichment line is optional: absent data (no tags, no sample, no
// RLS_PUBLIC_URL) simply drops the line. User-controlled text is escaped.
func (n *Notifier) postTelegram(ctx context.Context, evt Event, chatID, tgURL string) error {
	esc := html.EscapeString
	name := esc(evt.AgentName)
	phrase := firedPhrase[evt.Type]
	if evt.Kind == KindResolved {
		phrase = resolvedPhrase(evt.Type)
	} else if phrase == "" {
		phrase = strings.ToUpper(evt.Type)
	}
	lines := []string{}
	if evt.Kind == KindResolved {
		lines = append(lines, fmt.Sprintf("✅ <b>RLS · %s</b> — %s", name, esc(phrase)))
	} else {
		lines = append(lines, fmt.Sprintf("⚠️ <b>RLS · %s</b> — %s", name, esc(phrase)))
	}
	msg := evt.Message
	if msg == "" {
		msg = fmt.Sprintf("valor %.1f fuera de umbral", evt.Value)
	}
	lines = append(lines, "<i>"+esc(msg)+"</i>")
	if len(evt.Tags) > 0 {
		lines = append(lines, "🏷 "+esc(strings.Join(evt.Tags, ", ")))
	}
	var host []string
	if h := esc(short(evt.Hostname, 12)); h != "" {
		host = append(host, h)
	}
	if c := esc(short(evt.ContainerID, 12)); c != "" {
		host = append(host, c)
	}
	if len(host) > 0 {
		lines = append(lines, "🖥 "+strings.Join(host, " · "))
	}
	var stats []string
	if evt.LastCPU != nil {
		stats = append(stats, fmt.Sprintf("CPU %.1f%%", *evt.LastCPU))
	}
	if evt.LastRamUsed != nil {
		if evt.LastRamLim != nil {
			stats = append(stats, fmt.Sprintf("RAM %.1f/%.0f MB", *evt.LastRamUsed, *evt.LastRamLim))
		} else {
			stats = append(stats, fmt.Sprintf("RAM %.1f MB", *evt.LastRamUsed))
		}
	}
	if evt.UptimeSec != nil {
		stats = append(stats, "⏱ "+fmtUptime(*evt.UptimeSec))
	}
	if len(stats) > 0 {
		lines = append(lines, "📊 "+strings.Join(stats, " · "))
	}
	stamp := "generada"
	if evt.Kind == KindResolved {
		stamp = "resuelta"
	}
	if evt.TS > 0 {
		t := time.Unix(evt.TS, 0).UTC()
		lines = append(lines, fmt.Sprintf("%s %02d:%02d UTC", stamp, t.Hour(), t.Minute()))
	}
	if link := n.cfg.dashboardLink(evt.AgentID); link != "" {
		lines = append(lines, fmt.Sprintf("📊 <a href=%q>Panel completo</a>", esc(link)))
	}
	return n.post(ctx, tgURL, map[string]any{
		"chat_id": chatID, "parse_mode": "HTML", "text": strings.Join(lines, "\n"),
	})
}

func telegramEndpoint(token string) string {
	return "https://api.telegram.org/bot" + token + "/sendMessage"
}

// ---- Discord -------------------------------------------------------------

// postDiscord sends one webhook embed. Fired titles carry the short phrase
// ("🛰 {name} — sin comunicación", orange for thresholds); resolutions go
// green with "✅ {name} — señal restablecida / alerta resuelta". The
// enrichment fields (tags/Host·ID/last metrics/uptime/interval/relative
// ts) ride along, each omitted when the event lacks the data.
func (n *Notifier) postDiscord(ctx context.Context, evt Event, webhookURL string) error {
	desc := evt.Message
	if desc == "" {
		desc = fmt.Sprintf("valor %.1f fuera de umbral", evt.Value)
	}
	title, color := firedTitle(evt), discordColorFired
	if evt.Type != "offline" && evt.Kind != KindResolved {
		color = discordColorThreshold
	}
	if evt.Kind == KindResolved {
		title = fmt.Sprintf("✅ %s — %s", evt.AgentName, resolvedPhrase(evt.Type))
		color = discordColorResolved
	}
	embed := map[string]any{
		"title":       title,
		"description": desc,
		"color":       color,
		"timestamp":   time.Unix(evt.TS, 0).UTC().Format(time.RFC3339),
		"fields":      embedFields(evt),
	}
	// Footer keeps the brand and, when RLS_PUBLIC_URL is set, mirrors the
	// deep link (the clickable shortcut lives on the footer too).
	footer := "RLS Legacy"
	if link := n.cfg.dashboardLink(evt.AgentID); link != "" {
		footer = "RLS Legacy · tablero: " + link
	}
	embed["footer"] = map[string]any{"text": footer}
	return n.post(ctx, webhookURL, map[string]any{"embeds": []map[string]any{embed}})
}

// firedTitle renders the fired-alert headline: "🛰 {name} — sin comunicación"
// (or the threshold phrase; unknown types degrade to the raw type upper).
func firedTitle(evt Event) string {
	phrase := firedPhrase[evt.Type]
	if phrase == "" {
		phrase = strings.ToUpper(evt.Type)
	}
	return fmt.Sprintf("🛰 %s — %s", evt.AgentName, phrase)
}

// embedFields renders the enrichment as Discord embed fields. Inline rows
// for the numbers, code-styled Host/ID. Every absence (nil pointer, empty
// string, no tags) is silently skipped so a degraded event shows no holes.
func embedFields(evt Event) []map[string]any {
	fields := []map[string]any{}
	add := func(name, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		fields = append(fields, map[string]any{
			"name": name, "value": value, "inline": true,
		})
	}
	if len(evt.Tags) > 0 {
		add("Ecosistema", strings.Join(evt.Tags, ", "))
	}
	var host []string
	if h := short(evt.Hostname, 12); h != "" {
		host = append(host, "`"+h+"`")
	}
	if c := short(evt.ContainerID, 12); c != "" {
		host = append(host, "`"+c+"`")
	}
	if len(host) > 0 {
		add("Host / ID", strings.Join(host, " · "))
	}
	if evt.LastCPU != nil {
		add("Últ. CPU", fmt.Sprintf("%.1f %%", *evt.LastCPU))
	}
	if evt.LastRamUsed != nil {
		if evt.LastRamLim != nil {
			add("Últ. RAM", fmt.Sprintf("%.1f / %.0f MB", *evt.LastRamUsed, *evt.LastRamLim))
		} else {
			add("Últ. RAM", fmt.Sprintf("%.1f MB", *evt.LastRamUsed))
		}
	}
	if evt.UptimeSec != nil {
		add("Uptime", fmtUptime(*evt.UptimeSec))
	}
	if evt.Interval > 0 {
		add("Intervalo", fmt.Sprintf("%d s", evt.Interval))
	}
	if evt.TS > 0 {
		// Relative Discord timestamp (<t:unix:R> renders client-side, e.g.
		// "hace 30 segundos"); the embed timestamp itself is the absolute
		// fallback for clients that do not render inline markup.
		label := "Fue"
		if evt.Kind == KindResolved {
			label = "Resuelta"
		}
		add(label, fmt.Sprintf("<t:%d:R>", evt.TS))
	}
	return fields
}

// short truncates s to the n first runes (host/container ids rendering).
func short(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// fmtUptime renders a dry human process uptime: "23d 4h" / "2h 21m" /
// "12m 30s" / "45s".
func fmtUptime(sec int64) string {
	if sec < 0 {
		sec = 0
	}
	d := sec / 86400
	h := sec % 86400 / 3600
	m := sec % 3600 / 60
	s := sec % 60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// ---- shared HTTP plumbing ------------------------------------------------

// post marshals payload and POSTs it with the configured client timeout.
// Network errors are returned as-is (retryable); 3xx/4xx/5xx statuses become
// *statusError so the retry policy can classify them.
func (n *Notifier) post(ctx context.Context, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err // network / timeout: retryable by definition
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
	if resp.StatusCode >= 300 {
		return &statusError{code: resp.StatusCode, body: strings.TrimSpace(string(data))}
	}
	return nil
}

// SendTest delivers the test message "RLS test ok" to one channel — used by
// the admin endpoint POST /api/v1/notify/test. ErrNotConfigured when the
// channel has no credentials. Synchronous: the handler reports the outcome.
func (n *Notifier) SendTest(ctx context.Context, channel string) error {
	dOn, webhook, tgOn, chat, tgURL := n.current(ctx)
	switch channel {
	case "discord":
		if !dOn {
			return ErrNotConfigured
		}
		return n.post(ctx, webhook, map[string]any{
			"embeds": []map[string]any{{
				"title":     "🛰 RLS test ok",
				"color":     discordColorResolved,
				"timestamp": time.Now().UTC().Format(time.RFC3339),
			}},
		})
	case "telegram":
		if !tgOn {
			return ErrNotConfigured
		}
		return n.post(ctx, tgURL, map[string]any{
			"chat_id": chat, "parse_mode": "HTML",
			"text": "<b>RLS</b> test ok",
		})
	default:
		return fmt.Errorf("canal %q desconocido: usa 'discord' o 'telegram'", channel)
	}
}

// Stop closes the queue, aborts in-flight retries and waits up to grace for
// the worker to exit. Inert notifiers are a no-op. Call from main during
// graceful shutdown (before the store closes).
func (n *Notifier) Stop(grace time.Duration) {
	if n.ch == nil {
		return // inert
	}
	n.cancel() // fail in-flight attempts fast; queued events drain with ctx done
	close(n.ch)
	select {
	case <-n.done:
	case <-time.After(grace):
		slog.Warn("notifier drain timeout, exiting anyway")
	}
}
