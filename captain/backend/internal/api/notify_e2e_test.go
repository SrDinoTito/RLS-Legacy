package api_test

// End-to-end test of the REQ-050 notifier over the real API server:
// a fake Discord-style webhook captures the POSTs, an offline alert driven
// through a manual ticker Tick (the production path) reaches the webhook
// (verifying the retry ladder when the fake returns 500 twice and then 200),
// the resurrection beat notifies the resolution, and the admin test
// endpoint POST /api/v1/notify/test answers 201/503/400/401/403 as wired.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rls-legacy/captain/backend/internal/api"
	"github.com/rls-legacy/captain/backend/internal/ingest"
	"github.com/rls-legacy/captain/backend/internal/notify"
	"github.com/rls-legacy/captain/backend/internal/sse"
	"github.com/rls-legacy/captain/backend/internal/store"
)

// notifWebhook records every POST and answers per a scripted status list
// (the last status repeats forever).
type notifWebhook struct {
	mu       sync.Mutex
	statuses []int
	bodies   []string
	calls    int
}

func (f *notifWebhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

func (f *notifWebhook) state() (int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, append([]string(nil), f.bodies...)
}

func (f *notifWebhook) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv
}

// awaitCalls polls until the fake recorded want calls (deadline 5s: the
// retry ladder runs in the background with real 1s/2s backoff).
func awaitCalls(t *testing.T, f *notifWebhook, want int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if calls, _ := f.state(); calls >= want {
			_, bodies := f.state()
			return bodies
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls, bodies := f.state()
	t.Fatalf("fake webhook: waited for %d calls, got %d (%v)", want, calls, bodies)
	return nil
}

type discordEmbedField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type discordEmbed struct {
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Color       int                 `json:"color"`
	Timestamp   string              `json:"timestamp"`
	Fields      []discordEmbedField `json:"fields"`
	Footer      *struct {
		Text string `json:"text"`
	} `json:"footer"`
}

func decodeEmbed(t *testing.T, body string) discordEmbed {
	t.Helper()
	var payload struct {
		Embeds []discordEmbed `json:"embeds"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil || len(payload.Embeds) != 1 {
		t.Fatalf("unmarshal discord embed: %v (%s)", err, body)
	}
	return payload.Embeds[0]
}

// embedField returns the value of one named field ("" when the enrichment
// dropped it). name is exact.
func embedField(e discordEmbed, name string) string {
	for _, f := range e.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	return ""
}

// newNotifServer boots an api server whose notifier talks Discord/Telegram
// against the fake endpoint; documents the extra-notifier wiring. Returns
// the broker and notifier too, so the test can share them with the ticker
// exactly like main does.
func newNotifServer(t *testing.T, st *store.Store, fakeURL string) (*httptest.Server, *sse.Broker, *notify.Notifier) {
	t.Helper()
	notifier := notify.New(notify.Config{
		DiscordWebhookURL: fakeURL,
		QueueSize:         8,
		TimeoutSec:        5,
	})
	broker := sse.New()
	srv := httptest.NewServer(api.New(st, []byte("notif-secret"), broker, notifier).Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(func() { notifier.Stop(500 * time.Millisecond) })
	return srv, broker, notifier
}

func TestE2E_NotifierOfflineAlertWithRetry(t *testing.T) {
	// Script: fail twice with 500, then succeed → verifies 1 initial + 2
	// retries land as 3 calls and the alert notification survives.
	wb := &notifWebhook{statuses: []int{500, 500, 200}}
	fake := wb.server(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "notif.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	srv, broker, notifier := newNotifServer(t, st, fake.URL)

	seedToken(t, st, "rlst_notif_offline")
	if b := beat(t, srv, "rlst_notif_offline", strings.Replace(validBeat(), "1700000000000", "2500000000000", 1)); b.status != http.StatusOK {
		t.Fatalf("beat: %d %s", b.status, b.body)
	}

	// Silence → manual ticker tick (the production path: ingest.NewTicker +
	// FireOfflineAlert) → alert_new notification with retries.
	id, err := st.AgentByTokenHash(t.Context(), api.HashAgentToken("rlst_notif_offline"))
	if err != nil {
		t.Fatalf("agent lookup: %v", err)
	}
	if err := st.SetAgentLastSeen(t.Context(), id.ID, time.Now().Add(-500*time.Second).Unix()); err != nil {
		t.Fatalf("redate: %v", err)
	}
	ticker := ingest.NewTicker(st, time.Hour, broker, notifier)
	marked, err := ticker.Tick(t.Context())
	if err != nil || marked != 1 {
		t.Fatalf("tick: %v marked=%d", err, marked)
	}

	bodies := awaitCalls(t, wb, 3)
	if calls, _ := wb.state(); calls != 3 {
		t.Fatalf("want 3 calls (500,500,200 retry ladder), got %d", calls)
	}
	e := decodeEmbed(t, bodies[2])
	if e.Title != "🛰 nginx-prod — sin comunicación" {
		t.Fatalf("fired embed title: %q", e.Title)
	}
	if e.Color != 15008433 || e.Timestamp == "" || e.Description == "" {
		t.Fatalf("fired embed wrong: %+v", e)
	}
	if !strings.Contains(e.Description, "sin comunicación") {
		t.Fatalf("fired embed description: %q", e.Description)
	}
	// Enriched from the ticker path: the last stored sample (the beat's
	// metrics) rides along; no tags were seeded (field absent).
	if got := embedField(e, "Host / ID"); got != "`web-1` · `abc123`" {
		t.Fatalf("Host/ID field: %q", got)
	}
	if got := embedField(e, "Últ. CPU"); got != "12.4 %" {
		t.Fatalf("Últ. CPU field: %q", got)
	}
	if got := embedField(e, "Últ. RAM"); got != "145.2 / 512 MB" {
		t.Fatalf("Últ. RAM field: %q", got)
	}
	if got := embedField(e, "Uptime"); got != "2h 21m" {
		t.Fatalf("Uptime field: %q", got)
	}
	if got := embedField(e, "Intervalo"); got != "10 s" {
		t.Fatalf("Intervalo field: %q", got)
	}
	if got := embedField(e, "Fue"); got == "" || !strings.HasPrefix(got, "<t:") {
		t.Fatalf("Fue field: %q", got)
	}
	if embedField(e, "Ecosistema") != "" {
		t.Fatalf("tags field must be absent with no tags")
	}

	// Resurrection beat → the offline alert resolves → one more notification
	// in the resolved format (200 now: script last status repeats).
	if b := beat(t, srv, "rlst_notif_offline", strings.Replace(validBeat(), "1700000000000", "2500000100000", 1)); b.status != http.StatusOK {
		t.Fatalf("revive beat: %d %s", b.status, b.body)
	}
	bodies = awaitCalls(t, wb, 4)
	e = decodeEmbed(t, bodies[3])
	if e.Title != "✅ nginx-prod — señal restablecida" || e.Color != 3066993 {
		t.Fatalf("resolved embed wrong: %+v", e)
	}
	if got := embedField(e, "Resuelta"); got == "" || !strings.HasPrefix(got, "<t:") {
		t.Fatalf("resolved stamp field: %q", got)
	}
}

// TestE2E_NotifierVPSOutageAlertsEveryContainer proves the host-outage case:
// Stowaway reports each running container independently, so losing the VPS
// stops every heartbeat and the production offline ticker emits one Discord
// alert per adopted container. The single-container case is covered above.
func TestE2E_NotifierVPSOutageAlertsEveryContainer(t *testing.T) {
	wb := &notifWebhook{statuses: []int{200}}
	fake := wb.server(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "notif-vps-outage.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	srv, broker, notifier := newNotifServer(t, st, fake.URL)

	containers := []struct {
		token string
		name  string
		id    string
	}{
		{token: "rlst_vps_api", name: "api-prod", id: "container-api"},
		{token: "rlst_vps_worker", name: "worker-prod", id: "container-worker"},
	}
	for _, container := range containers {
		seedToken(t, st, container.token)
		body := strings.Replace(validBeat(), `"hostname": "web-1"`, `"hostname": "vps-prod-01"`, 1)
		body = strings.Replace(body, `"name": "nginx-prod"`, `"name": "`+container.name+`"`, 1)
		body = strings.Replace(body, `"container_id": "abc123"`, `"container_id": "`+container.id+`"`, 1)
		body = strings.Replace(body, "1700000000000", "2500000000000", 1)
		if response := beat(t, srv, container.token, body); response.status != http.StatusOK {
			t.Fatalf("heartbeat %s: %d %s", container.name, response.status, response.body)
		}
		agent, err := st.AgentByTokenHash(t.Context(), api.HashAgentToken(container.token))
		if err != nil {
			t.Fatalf("lookup %s: %v", container.name, err)
		}
		if err := st.SetAgentLastSeen(t.Context(), agent.ID, time.Now().Add(-500*time.Second).Unix()); err != nil {
			t.Fatalf("silence %s: %v", container.name, err)
		}
	}

	ticker := ingest.NewTicker(st, time.Hour, broker, notifier)
	marked, err := ticker.Tick(t.Context())
	if err != nil || marked != len(containers) {
		t.Fatalf("whole VPS outage: marked=%d want=%d err=%v", marked, len(containers), err)
	}

	bodies := awaitCalls(t, wb, len(containers))
	got := make(map[string]bool, len(bodies))
	for _, body := range bodies {
		embed := decodeEmbed(t, body)
		if !strings.Contains(embed.Title, "sin comunicación") {
			t.Fatalf("expected offline alert title, got %q", embed.Title)
		}
		if !strings.Contains(embed.Title, "api-prod") && !strings.Contains(embed.Title, "worker-prod") {
			t.Fatalf("alert missing container identity: %q", embed.Title)
		}
		got[embed.Title] = true
	}
	if len(got) != len(containers) {
		t.Fatalf("whole VPS outage generated %d unique alerts, want %d: %v", len(got), len(containers), got)
	}
}

// TestE2E_NotifierSingleContainerOutageIsScoped verifies that a container
// failure on a VPS alerts only that agent while its sibling keeps reporting.
func TestE2E_NotifierSingleContainerOutageIsScoped(t *testing.T) {
	wb := &notifWebhook{statuses: []int{200}}
	fake := wb.server(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "notif-single-container.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	srv, broker, notifier := newNotifServer(t, st, fake.URL)

	containers := []struct {
		token string
		name  string
		id    string
	}{
		{token: "rlst_partial_api", name: "api-prod", id: "container-api"},
		{token: "rlst_partial_worker", name: "worker-prod", id: "container-worker"},
	}
	var siblingID int64
	for i, container := range containers {
		seedToken(t, st, container.token)
		body := strings.Replace(validBeat(), `"hostname": "web-1"`, `"hostname": "vps-prod-01"`, 1)
		body = strings.Replace(body, `"name": "nginx-prod"`, `"name": "`+container.name+`"`, 1)
		body = strings.Replace(body, `"container_id": "abc123"`, `"container_id": "`+container.id+`"`, 1)
		body = strings.Replace(body, "1700000000000", "2500000000000", 1)
		if response := beat(t, srv, container.token, body); response.status != http.StatusOK {
			t.Fatalf("heartbeat %s: %d %s", container.name, response.status, response.body)
		}
		agent, err := st.AgentByTokenHash(t.Context(), api.HashAgentToken(container.token))
		if err != nil {
			t.Fatalf("lookup %s: %v", container.name, err)
		}
		if i == 0 {
			if err := st.SetAgentLastSeen(t.Context(), agent.ID, time.Now().Add(-500*time.Second).Unix()); err != nil {
				t.Fatalf("silence %s: %v", container.name, err)
			}
		} else {
			siblingID = agent.ID
		}
	}

	ticker := ingest.NewTicker(st, time.Hour, broker, notifier)
	marked, err := ticker.Tick(t.Context())
	if err != nil || marked != 1 {
		t.Fatalf("single container outage: marked=%d want=1 err=%v", marked, err)
	}
	bodies := awaitCalls(t, wb, 1)
	if len(bodies) != 1 {
		t.Fatalf("single container outage sent %d notifications, want 1", len(bodies))
	}
	if title := decodeEmbed(t, bodies[0]).Title; !strings.Contains(title, "api-prod") || strings.Contains(title, "worker-prod") {
		t.Fatalf("notification should identify only the failed container, got %q", title)
	}
	sibling, err := st.AgentByID(t.Context(), siblingID)
	if err != nil || sibling.Status != "online" {
		t.Fatalf("healthy sibling status=%q err=%v, want online", sibling.Status, err)
	}
}

// TestE2E_NotifierEnrichedTagsAndLink wires a notifier with RLS_PUBLIC_URL
// and an agent carrying ecosystem tags: the fired embed must expose the
// tags field and the footer deep link to the agent detail view.
func TestE2E_NotifierEnrichedTagsAndLink(t *testing.T) {
	wb := &notifWebhook{statuses: []int{200}}
	fake := wb.server(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "notif-link.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	const publicURL = "https://panel.example"
	notifier := notify.New(notify.Config{
		DiscordWebhookURL: fake.URL, PublicURL: publicURL,
		QueueSize: 8, TimeoutSec: 5,
	})
	broker := sse.New()
	srv := httptest.NewServer(api.New(st, []byte("notif-secret"), broker, notifier,
		notify.Config{PublicURL: publicURL}).Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(func() { notifier.Stop(500 * time.Millisecond) })

	seedToken(t, st, "rlst_notif_link")
	if b := beat(t, srv, "rlst_notif_link", strings.Replace(validBeat(), "1700000000000", "2501100000000", 1)); b.status != http.StatusOK {
		t.Fatalf("beat: %d %s", b.status, b.body)
	}
	id, err := st.AgentByTokenHash(t.Context(), api.HashAgentToken("rlst_notif_link"))
	if err != nil {
		t.Fatalf("agent lookup: %v", err)
	}
	if err := st.SetAgentTags(t.Context(), id.ID, `["lab","prod"]`); err != nil {
		t.Fatalf("set tags: %v", err)
	}
	if err := st.SetAgentLastSeen(t.Context(), id.ID, time.Now().Add(-500*time.Second).Unix()); err != nil {
		t.Fatalf("redate: %v", err)
	}
	ticker := ingest.NewTicker(st, time.Hour, broker, notifier)
	if marked, err := ticker.Tick(t.Context()); err != nil || marked != 1 {
		t.Fatalf("tick: %v marked=%d", err, marked)
	}

	bodies := awaitCalls(t, wb, 1)
	e := decodeEmbed(t, bodies[0])
	if e.Title != "🛰 nginx-prod — sin comunicación" || embedField(e, "Ecosistema") != "lab, prod" {
		t.Fatalf("tags/link embed wrong: %+v", e)
	}
	if e.Footer == nil || e.Footer.Text != "RLS Legacy · tablero: "+publicURL+"/agents/"+fmt.Sprintf("%d", id.ID) {
		t.Fatalf("footer deep link: %+v", e.Footer)
	}
}

func TestE2E_NotifyTestEndpoint(t *testing.T) {
	wb := &notifWebhook{statuses: []int{200}}
	fake := wb.server(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "notif-test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	srv, _, _ := newNotifServer(t, st, fake.URL)
	base := srv.URL

	// No cookie → 401 (cookie auth required).
	if r := do(t, nil, http.MethodPost, base+"/api/v1/notify/test", `{"channel":"discord"}`); r.status != http.StatusUnauthorized {
		t.Fatalf("anon notify/test: got %d %s", r.status, r.body)
	}
	admin := bootstrapAdmin(t, srv)

	// Viewer → 403 (admin-only).
	if r := do(t, admin, http.MethodPost, base+"/api/v1/users",
		`{"username":"viewer1","password":"viewer-password-1","role":"viewer"}`); r.status != http.StatusCreated {
		t.Fatalf("create viewer: %d %s", r.status, r.body)
	}
	viewer := clientLoggedIn(t, srv, "viewer1", "viewer-password-1")
	if r := do(t, viewer, http.MethodPost, base+"/api/v1/notify/test", `{"channel":"discord"}`); r.status != http.StatusForbidden {
		t.Fatalf("viewer notify/test: got %d %s", r.status, r.body)
	}

	// Unknown channel → 400.
	if r := do(t, admin, http.MethodPost, base+"/api/v1/notify/test", `{"channel":"banana"}`); r.status != http.StatusBadRequest {
		t.Fatalf("unknown channel: got %d %s", r.status, r.body)
	}

	// Telegram NOT configured in this fixture → 503 with a clear message.
	if r := do(t, admin, http.MethodPost, base+"/api/v1/notify/test", `{"channel":"telegram"}`); r.status != http.StatusServiceUnavailable || !strings.Contains(r.body, "not configured") {
		t.Fatalf("unconfigured telegram: got %d %s", r.status, r.body)
	}

	// Discord configured (fake webhook returns 200) → 201 + delivery.
	if r := do(t, admin, http.MethodPost, base+"/api/v1/notify/test", `{"channel":"discord"}`); r.status != http.StatusCreated || !strings.Contains(r.body, `"status":"ok"`) {
		t.Fatalf("notify test ok: got %d %s", r.status, r.body)
	}
	bodies := awaitCalls(t, wb, 1)
	if !strings.Contains(bodies[0], "RLS test ok") {
		t.Fatalf("test message not delivered: %s", bodies[0])
	}
}
