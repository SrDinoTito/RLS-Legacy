package notify

// Unit tests of the notifier against a fake backing HTTP backend: retry
// ladder on 5xx, no-retry on 4xx (config error, warned once), non-blocking
// Enqueue with drop counter, inert notifier without channels and the
// admin SendTest helper.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackend answers POSTs with a scripted status list (the last status
// repeats forever) and records every body it received.
type fakeBackend struct {
	mu       sync.Mutex
	statuses []int
	bodies   []string
	calls    int
}

func newFakeBackend(statuses ...int) *fakeBackend {
	return &fakeBackend{statuses: statuses}
}

func (f *fakeBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	f.mu.Lock()
	idx := f.calls
	f.calls++
	f.bodies = append(f.bodies, string(body))
	code := f.statuses[len(f.statuses)-1]
	if idx < len(f.statuses) {
		code = f.statuses[idx]
	}
	f.mu.Unlock()
	w.WriteHeader(code)
}

func (f *fakeBackend) state() (int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, append([]string(nil), f.bodies...)
}

func (f *fakeBackend) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv
}

// newTestNotifier builds a configured notifier pointing every enabled
// channel at the fake endpoints, with a millisecond retry ladder (the
// production ladder is 1s/2s/4s; tests shrink it). t.Cleanup stops it.
func newTestNotifier(t *testing.T, cfg Config, discordURL, telegramURL string) *Notifier {
	t.Helper()
	n := New(cfg)
	n.backoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	if n.discordEnabled {
		n.cfg.DiscordWebhookURL = discordURL
	}
	if n.telegramEnabled {
		n.telegramURL = telegramURL
	}
	t.Cleanup(func() { n.Stop(time.Second) })
	return n
}

// testEvent is the canonical offline event used by most tests: enriched
// (tags/host/metrics) so the formatter tests can assert the embed fields and
// telegram lines; TS is fixed so any "generada HH:MM UTC" render is
// deterministic (1700000000 → 22:13 UTC).
func testEvent() Event {
	cpu, used, lim, uptime := 12.4, 145.2, 512.0, int64(8472) // 2h 21m
	return Event{
		AgentID: 1, AgentName: "mi-nginx-prod", Type: "offline",
		Value: 45.5, Message: "sin comunicación (posible caída)",
		TS: 1700000000, Kind: KindNew,
		Tags:        []string{"lab", "infra"},
		Hostname:    "web-1",
		ContainerID: "abc123def456789", // deliberate >12: short() must trim
		Interval:    10,
		LastCPU:     &cpu, LastRamUsed: &used, LastRamLim: &lim, UptimeSec: &uptime,
	}
}

// awaitBodies polls until the backend recorded want calls (deadline 2s).
func awaitBodies(t *testing.T, f *fakeBackend, want int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls, _ := f.state(); calls >= want {
			_, bodies := f.state()
			return bodies
		}
		time.Sleep(2 * time.Millisecond)
	}
	calls, bodies := f.state()
	t.Fatalf("fake backend: waited for %d calls, got %d (%v)", want, calls, bodies)
	return nil
}

func TestEnqueueInertWithoutChannels(t *testing.T) {
	n := New(Config{}) // no channels configured at all
	t.Cleanup(func() { n.Stop(0) })
	if n.ch != nil {
		t.Fatalf("inert notifier must not spawn a queue")
	}
	if n.discordEnabled || n.telegramEnabled {
		t.Fatalf("inert notifier must not enable channels")
	}
	// Enqueue is a silent no-op (no panic, no drop counter bump).
	n.Enqueue(testEvent())
	if n.dropped.Load() != 0 {
		t.Fatalf("inert drop counter changed: %d", n.dropped.Load())
	}
}

func TestTelegramChannelNeedsBothVariables(t *testing.T) {
	n := New(Config{TelegramBotToken: "tok"}) // chat id missing → inert
	t.Cleanup(func() { n.Stop(0) })
	if n.telegramEnabled {
		t.Fatalf("telegram without chat id must be disabled")
	}
}

func TestEnqueueNonBlockingWithDropCounter(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan struct{}, 8)
	f := newFakeBackend(200)
	// Block the delivery attempt until the test is done: the handler signals
	// entry, then parks on the gate.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-gate
		f.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	n := New(Config{DiscordWebhookURL: srv.URL, QueueSize: 2})
	t.Cleanup(func() { n.Stop(time.Second) })
	n.backoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	n.cfg.DiscordWebhookURL = srv.URL

	n.Enqueue(testEvent())
	<-entered // the worker consumed one slot and is parked inside deliver
	// Now the 2-slot buffer is free: fill both and prove the third blocks
	// by dropping (and only by dropping — never by blocking the caller).
	n.Enqueue(Event{AgentID: 1, AgentName: "b", Type: "high_cpu", Kind: KindNew})
	n.Enqueue(Event{AgentID: 1, AgentName: "c", Type: "high_ram", Kind: KindNew})
	n.Enqueue(testEvent()) // queue full → dropped; must NOT block
	if got := n.dropped.Load(); got != 1 {
		t.Fatalf("drop counter: want 1, got %d", got)
	}
	close(gate) // unblock delivery so Stop() can drain
}

func TestDiscordRetryOn5xxThenSuccess(t *testing.T) {
	f := newFakeBackend(500, 500, 200) // two failures, third succeeds
	srv := f.server(t)
	n := newTestNotifier(t, Config{DiscordWebhookURL: srv.URL}, srv.URL, "")

	n.Enqueue(testEvent())
	bodies := awaitBodies(t, f, 3)
	if calls, _ := f.state(); calls != 3 {
		t.Fatalf("retry ladder: want exactly 3 calls, got %d", calls)
	}
	var payload struct {
		Embeds []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Color       int    `json:"color"`
			Timestamp   string `json:"timestamp"`
			Fields      []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"fields"`
			Footer *struct {
				Text string `json:"text"`
			} `json:"footer"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal([]byte(bodies[2]), &payload); err != nil {
		t.Fatalf("unmarshal embed: %v (%s)", err, bodies[2])
	}
	if len(payload.Embeds) != 1 {
		t.Fatalf("want one embed, got %d", len(payload.Embeds))
	}
	e := payload.Embeds[0]
	if e.Title != "🛰 mi-nginx-prod — sin comunicación" {
		t.Fatalf("embed title: %q", e.Title)
	}
	if e.Color != 15008433 {
		t.Fatalf("fired embed color: %d", e.Color)
	}
	if e.Description != "sin comunicación (posible caída)" || e.Timestamp == "" {
		t.Fatalf("embed content wrong: %+v", e)
	}
	// Enriched fields: tags joined, code-styled host/id (trimmed to 12),
	// last sample metrics, dry uptime and the relative Discord timestamp.
	wantFields := [][2]string{
		{"Ecosistema", "lab, infra"},
		{"Host / ID", "`web-1` · `abc123def456`"},
		{"Últ. CPU", "12.4 %"},
		{"Últ. RAM", "145.2 / 512 MB"},
		{"Uptime", "2h 21m"},
		{"Intervalo", "10 s"},
	}
	if len(e.Fields) != len(wantFields)+1 { // + the relative "Fue" stamp
		t.Fatalf("embed fields: want %d rows, got %d (%+v)", len(wantFields)+1, len(e.Fields), e.Fields)
	}
	for i, wf := range wantFields {
		if e.Fields[i].Name != wf[0] || e.Fields[i].Value != wf[1] {
			t.Fatalf("field %d: want %q=%q, got %q=%q", i, wf[0], wf[1], e.Fields[i].Name, e.Fields[i].Value)
		}
	}
	last := e.Fields[len(e.Fields)-1]
	if last.Name != "Fue" || !strings.HasPrefix(last.Value, "<t:1700000000") {
		t.Fatalf("relative stamp field: %+v", last)
	}
	if e.Footer == nil || e.Footer.Text != "RLS Legacy" {
		t.Fatalf("footer without RLS_PUBLIC_URL: %+v", e.Footer)
	}
}

// TestDiscordDegradedFields covers the graceful degradation: an event with
// NO enrichment (no tags/hostname/sample, e.g. an agent that never beat)
// renders only delivery-safe minimal content — no empty placeholder rows.
func TestDiscordDegradedFields(t *testing.T) {
	f := newFakeBackend(200)
	srv := f.server(t)
	n := newTestNotifier(t, Config{DiscordWebhookURL: srv.URL}, srv.URL, "")

	n.Enqueue(Event{
		AgentID: 9, AgentName: "ghost-agent", Type: "high_cpu",
		Value: 95.1, Message: "CPU alta: valor 95.1 supera umbral 90",
		TS: 1700000000, Kind: KindNew,
	})
	bodies := awaitBodies(t, f, 1)
	var payload struct {
		Embeds []struct {
			Title  string `json:"title"`
			Color  int    `json:"color"`
			Fields []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"fields"`
			Footer *struct {
				Text string `json:"text"`
			} `json:"footer"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &payload); err != nil {
		t.Fatalf("unmarshal embed: %v (%s)", err, bodies[0])
	}
	e := payload.Embeds[0]
	if e.Title != "🛰 ghost-agent — CPU sobre umbral" {
		t.Fatalf("threshold title: %q", e.Title)
	}
	if e.Color != discordColorThreshold {
		t.Fatalf("threshold color: %d", e.Color)
	}
	if len(e.Fields) != 1 || e.Fields[0].Name != "Fue" {
		t.Fatalf("degraded event must carry no optional fields: %+v", e.Fields)
	}
	if e.Footer == nil || e.Footer.Text != "RLS Legacy" {
		t.Fatalf("degraded footer: %+v", e.Footer)
	}
}

// TestDiscordFooterLinkChecks the RLS_PUBLIC_URL behavior: trailing slash is
// normalized and the deep link points at the agent detail view.
func TestDiscordFooterLinkChecks(t *testing.T) {
	f := newFakeBackend(200)
	srv := f.server(t)
	n := newTestNotifier(t, Config{DiscordWebhookURL: srv.URL, PublicURL: "https://panel.example/"}, srv.URL, "")

	n.Enqueue(Event{
		AgentID: 7, AgentName: "linked", Type: "offline",
		Message: "sin comunicación (posible caída)",
		TS:      1700000000, Kind: KindNew,
	})
	bodies := awaitBodies(t, f, 1)
	if !strings.Contains(bodies[0], `"text":"RLS Legacy · tablero: https://panel.example/agents/7"`) {
		t.Fatalf("footer link missing: %s", bodies[0])
	}
}

func TestDiscord4xxNeverRetried(t *testing.T) {
	f := newFakeBackend(400) // bad webhook forever
	srv := f.server(t)
	n := newTestNotifier(t, Config{DiscordWebhookURL: srv.URL}, srv.URL, "")

	n.Enqueue(testEvent())
	awaitBodies(t, f, 1)
	time.Sleep(30 * time.Millisecond) // give any (wrong) retry a chance
	if calls, bodies := f.state(); calls != 1 {
		t.Fatalf("4xx must never retry: got %d calls (%v)", calls, bodies)
	}
}

func TestResolvedEventUsesResolvedFormat(t *testing.T) {
	f := newFakeBackend(200)
	srv := f.server(t)
	n := newTestNotifier(t, Config{DiscordWebhookURL: srv.URL}, srv.URL, "")

	n.Enqueue(Event{
		AgentID: 1, AgentName: "mi-nginx-prod", Type: "offline",
		Message: "agente de nuevo en línea", TS: 1700000000,
		Kind: KindResolved,
	})
	bodies := awaitBodies(t, f, 1)
	var payload struct {
		Embeds []struct {
			Title string `json:"title"`
			Color int    `json:"color"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &payload); err != nil {
		t.Fatalf("unmarshal embed: %v (%s)", err, bodies[0])
	}
	e := payload.Embeds[0]
	if e.Title != "✅ mi-nginx-prod — señal restablecida" || e.Color != discordColorResolved {
		t.Fatalf("resolved embed wrong: %+v", e)
	}
}

func TestTelegramDelivery(t *testing.T) {
	f := newFakeBackend(200)
	srv := f.server(t)
	n := newTestNotifier(t, Config{
		TelegramBotToken: "123:tst", TelegramChatID: "-100200",
	}, "", srv.URL)

	n.Enqueue(testEvent())
	bodies := awaitBodies(t, f, 1)
	var payload struct {
		ChatID    string `json:"chat_id"`
		ParseMode string `json:"parse_mode"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &payload); err != nil {
		t.Fatalf("unmarshal telegram: %v (%s)", err, bodies[0])
	}
	if payload.ChatID != "-100200" || payload.ParseMode != "HTML" {
		t.Fatalf("telegram args wrong: %+v", payload)
	}
	want := strings.Join([]string{
		"⚠️ <b>RLS · mi-nginx-prod</b> — sin comunicación",
		"<i>sin comunicación (posible caída)</i>",
		"🏷 lab, infra",
		"🖥 web-1 · abc123def456",
		"📊 CPU 12.4% · RAM 145.2/512 MB · ⏱ 2h 21m",
		"generada 22:13 UTC",
	}, "\n")
	if payload.Text != want {
		t.Fatalf("telegram text:\n got %q\nwant %q", payload.Text, want)
	}
}

// TestTelegramLinkAndResolvedFormat covers the "Panel completo" link (only
// with RLS_PUBLIC_URL set) and the resolved headline/wording.
func TestTelegramLinkAndResolvedFormat(t *testing.T) {
	f := newFakeBackend(200)
	srv := f.server(t)
	n := newTestNotifier(t, Config{
		TelegramBotToken: "123:tst", TelegramChatID: "-100200",
		PublicURL: "https://panel.example",
	}, "", srv.URL)

	n.Enqueue(testEvent())
	bodies := awaitBodies(t, f, 1)
	var linkPayload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &linkPayload); err != nil {
		t.Fatalf("unmarshal telegram link: %v (%s)", err, bodies[0])
	}
	if !strings.Contains(linkPayload.Text, `📊 <a href="https://panel.example/agents/1">Panel completo</a>`) {
		t.Fatalf("telegram link line missing: %q", linkPayload.Text)
	}

	n.Enqueue(Event{
		AgentID: 1, AgentName: "mi-nginx-prod", Type: "high_cpu",
		Message: "agente de nuevo en línea", TS: 1700000000,
		Kind: KindResolved,
	})
	bodies2 := awaitBodies(t, f, 2)
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(bodies2[1]), &payload); err != nil {
		t.Fatalf("unmarshal telegram resolved: %v", err)
	}
	want := strings.Join([]string{
		"✅ <b>RLS · mi-nginx-prod</b> — alerta resuelta",
		"<i>agente de nuevo en línea</i>",
		"resuelta 22:13 UTC",
		`📊 <a href="https://panel.example/agents/1">Panel completo</a>`,
	}, "\n")
	if payload.Text != want {
		t.Fatalf("telegram resolved text:\n got %q\nwant %q", payload.Text, want)
	}
}

func TestTelegramRetryOn5xx(t *testing.T) {
	f := newFakeBackend(503, 503, 200)
	srv := f.server(t)
	n := newTestNotifier(t, Config{
		TelegramBotToken: "123:tst", TelegramChatID: "42",
	}, "", srv.URL)

	n.Enqueue(testEvent())
	awaitBodies(t, f, 3)
	if calls, _ := f.state(); calls != 3 {
		t.Fatalf("telegram retry ladder: want 3 calls, got %d", calls)
	}
}

func TestNonNotifyTypeSilentlyDropped(t *testing.T) {
	f := newFakeBackend(200)
	srv := f.server(t)
	n := newTestNotifier(t, Config{DiscordWebhookURL: srv.URL}, srv.URL, "")

	n.Enqueue(Event{AgentName: "x", Type: "heartbeat", Kind: KindNew})
	n.Enqueue(Event{AgentName: "x", Type: "agent_online", Kind: KindResolved})
	time.Sleep(30 * time.Millisecond)
	if calls, _ := f.state(); calls != 0 {
		t.Fatalf("non-alert events must not be notified: %d calls", calls)
	}
}

func TestSendTest(t *testing.T) {
	f := newFakeBackend(200)
	srv := f.server(t)
	n := newTestNotifier(t, Config{DiscordWebhookURL: srv.URL}, srv.URL, "")

	if err := n.SendTest(context.Background(), "discord"); err != nil {
		t.Fatalf("SendTest discord: %v", err)
	}
	bodies := awaitBodies(t, f, 1)
	if !strings.Contains(bodies[0], "RLS test ok") {
		t.Fatalf("test message not sent: %s", bodies[0])
	}
	if err := n.SendTest(context.Background(), "telegram"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("SendTest unconfigured telegram: %v", err)
	}
	if err := n.SendTest(context.Background(), "banana"); err == nil {
		t.Fatalf("SendTest unknown channel must error")
	}

	inert := New(Config{})
	t.Cleanup(func() { inert.Stop(0) })
	if err := inert.SendTest(context.Background(), "discord"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("inert SendTest: %v", err)
	}
}
