package api_test

// E2E of the dashboard-driven notification settings (REQ-050 flow v2):
//
//	PUT /notify/settings (discord válido + telegram token/chat validados)
//	  → GET devuelve SIEMPRE valores enmascarados (last8/last4, jamás
//	  plaintext), → alerta offline via ticker Tick (ruta de producción) aterriza
//	  en un fake webhook y un fake telegram (httptest) → PUT con vacíos
//	  deshabilita los canales → una alerta nueva NO genera llamadas → env
//	  fallback: DB vacía + RLS_NOTIF_* enviados → sí notifica vía fake.
//
// El redirect transport re-apunta las llamadas salientes del notifier
// (discord.com / api.telegram.org) hacia los fakes preservando path, headers
// y body, para poder validar también la forma real de las credenciales.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rls-legacy/captain/backend/internal/api"
	"github.com/rls-legacy/captain/backend/internal/ingest"
	"github.com/rls-legacy/captain/backend/internal/notify"
	"github.com/rls-legacy/captain/backend/internal/sse"
	"github.com/rls-legacy/captain/backend/internal/store"
)

// redirectRT points the notifier's outbound calls at the in-test fakes:
// any request toward a registered host (discord.com, api.telegram.org) is
// re-hosted onto the matching httptest server.
type redirectRT struct {
	targets map[string]*httptest.Server
}

func (r redirectRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if srv, ok := r.targets[req.URL.Host]; ok {
		var body []byte
		if req.Body != nil {
			body, _ = io.ReadAll(req.Body)
			req.Body.Close()
		}
		u, err := url.Parse(srv.URL)
		if err != nil {
			return nil, err
		}
		nr := req.Clone(req.Context())
		nr.Host = u.Host
		nr.URL.Host = u.Host
		nr.URL.Scheme = u.Scheme
		if len(body) > 0 {
			nr.Body = io.NopCloser(bytes.NewReader(body))
		} else {
			nr.Body = http.NoBody
		}
		req = nr
	}
	return http.DefaultTransport.RoundTrip(req)
}

// newFakePair builds the two capture fakes and the redirect transport that
// maps the real channel hosts onto them (bodies + status preserved).
func newFakePair(t *testing.T) (discord, telegram *notifWebhook, rt redirectRT) {
	t.Helper()
	discord = &notifWebhook{statuses: []int{200}}
	telegram = &notifWebhook{statuses: []int{200}}
	rt = redirectRT{targets: map[string]*httptest.Server{
		"discord.com":      discord.server(t),
		"api.telegram.org": telegram.server(t),
	}}
	return discord, telegram, rt
}

// newNotifySettingsServer boots the full dashboard stack (api + store +
// dynamic notifier) exactly like main.go wires it, but with the notifier's
// HTTP client routed through rt. Returns the parts the test drives.
func newNotifySettingsServer(t *testing.T, env notify.Config, rt redirectRT) (*httptest.Server, *store.Store, *sse.Broker, *notify.Notifier) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "notifset.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}
	notifier := notify.NewDynamic(env, func(ctx context.Context) (notify.Settings, error) {
		row, err := st.GetNotifySettings(ctx)
		if err != nil {
			return notify.Settings{}, err
		}
		return notify.Settings{
			DiscordWebhookURL: row.DiscordWebhookURL,
			TelegramBotToken:  row.TelegramBotToken,
			TelegramChatID:    row.TelegramChatID,
		}, nil
	}, client)
	broker := sse.New()
	srv := httptest.NewServer(api.New(st, []byte("notifset-secret"), broker, notifier, env).Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(func() { notifier.Stop(500 * time.Millisecond) })
	return srv, st, broker, notifier
}

// fireOfflineAlert drives the production path once: seed token → beat →
// silence + redate → manual ticker tick → offline alert (SSE + notifier).
func fireOfflineAlert(t *testing.T, srv *httptest.Server, st *store.Store, broker *sse.Broker, n *notify.Notifier, token, ts string) {
	t.Helper()
	seedToken(t, st, token)
	if b := beat(t, srv, token, strings.Replace(validBeat(), "1700000000000", ts, 1)); b.status != http.StatusOK {
		t.Fatalf("beat %s: %d %s", token, b.status, b.body)
	}
	id, err := st.AgentByTokenHash(t.Context(), api.HashAgentToken(token))
	if err != nil {
		t.Fatalf("agent lookup: %v", err)
	}
	if err := st.SetAgentLastSeen(t.Context(), id.ID, time.Now().Add(-500*time.Second).Unix()); err != nil {
		t.Fatalf("redate: %v", err)
	}
	ticker := ingest.NewTicker(st, time.Hour, broker, n)
	marked, err := ticker.Tick(t.Context())
	if err != nil || marked != 1 {
		t.Fatalf("tick: %v marked=%d", err, marked)
	}
}

func TestE2E_NotifySettingsDashboardChannels(t *testing.T) {
	discord, telegram, rt := newFakePair(t)
	srv, st, broker, notifier := newNotifySettingsServer(t, notify.Config{}, rt)
	base := srv.URL

	admin := bootstrapAdmin(t, srv)

	// Viewer → 403 on the settings endpoints (admin-only).
	if r := do(t, admin, http.MethodPost, base+"/api/v1/users",
		`{"username":"viewer2","password":"viewer-password-2","role":"viewer"}`); r.status != http.StatusCreated {
		t.Fatalf("create viewer: %d %s", r.status, r.body)
	}
	viewer := clientLoggedIn(t, srv, "viewer2", "viewer-password-2")
	if r := do(t, viewer, http.MethodGet, base+"/api/v1/notify/settings", ""); r.status != http.StatusForbidden {
		t.Fatalf("viewer GET settings: got %d %s", r.status, r.body)
	}
	if r := do(t, viewer, http.MethodPut, base+"/api/v1/notify/settings", `{}`); r.status != http.StatusForbidden {
		t.Fatalf("viewer PUT settings: got %d %s", r.status, r.body)
	}

	// Boot: nothing configured → all nulls + disabled.
	if r := do(t, admin, http.MethodGet, base+"/api/v1/notify/settings", ""); r.status != http.StatusOK {
		t.Fatalf("GET settings: got %d %s", r.status, r.body)
	}

	// Validación: webhook con prefijo incorrecto → 400; token/chat inválidos → 400.
	if r := do(t, admin, http.MethodPut, base+"/api/v1/notify/settings",
		`{"discord_webhook_url":"https://evil.example/webhooks/1/abc"}`); r.status != http.StatusBadRequest {
		t.Fatalf("invalid discord webhook: got %d %s", r.status, r.body)
	}
	if r := do(t, admin, http.MethodPut, base+"/api/v1/notify/settings",
		`{"telegram_bot_token":" clearly-not-a-token "}`); r.status != http.StatusBadRequest {
		t.Fatalf("invalid telegram token: got %d %s", r.status, r.body)
	}
	if r := do(t, admin, http.MethodPut, base+"/api/v1/notify/settings",
		`{"telegram_chat_id":"abc"}`); r.status != http.StatusBadRequest {
		t.Fatalf("invalid chat id: got %d %s", r.status, r.body)
	}

	// PUT válido: discord webhook + telegram token/chat (mismo cuerpo real).
	if r := do(t, admin, http.MethodPut, base+"/api/v1/notify/settings",
		`{"discord_webhook_url":"https://discord.com/api/webhooks/000000000000000000/TEST-WEBHOOK-BBccDDxx",
		"telegram_bot_token":"987654321:tStUvWxYz","telegram_chat_id":"-100"}`); r.status != http.StatusOK {
		t.Fatalf("PUT settings: got %d %s", r.status, r.body)
	}

	// GET → SIEMPRE maskado. Implacable: el plaintext jamás sale del backend.
	get := func() map[string]any {
		r := do(t, admin, http.MethodGet, base+"/api/v1/notify/settings", "")
		if r.status != http.StatusOK {
			t.Fatalf("GET settings: got %d %s", r.status, r.body)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(r.body), &m); err != nil {
			t.Fatalf("unmarshal settings: %v (%s)", err, r.body)
		}
		return m
	}
	m := get()
	if got := m["discord_webhook_url"]; got != "BBccDDxx" {
		t.Fatalf("discord mask: want last 8 BBccDDxx, got %v", got)
	}
	if got := m["telegram_bot_token"]; got != "WxYz" {
		t.Fatalf("token mask: want last 4 WxYz, got %v", got)
	}
	if got := m["telegram_chat_id"]; got != "-100" {
		t.Fatalf("chat id: want -100, got %v", got)
	}
	if m["discord_enabled"] != true || m["telegram_enabled"] != true {
		t.Fatalf("enabled flags: %+v", m)
	}
	if strings.Contains(marshalNeed(t, m), "111222333/aaBBccDDxx") || strings.Contains(marshalNeed(t, m), "987654321:") {
		t.Fatalf("settings leaked plaintext credentials: %+v", m)
	}

	// Alerta via Tick (ruta de producción) → fake webhook + fake telegram
	// reciben la notificación (embed + sendMessage en su formato real).
	fireOfflineAlert(t, srv, st, broker, notifier, "rlst_notifset_off", "2500100000000")
	bodies := awaitCalls(t, discord, 1)
	embed := decodeEmbed(t, bodies[0])
	if embed.Title != "🛰 nginx-prod — sin comunicación" {
		t.Fatalf("discord embed off: %+v", embed)
	}
	tgBodies := awaitCalls(t, telegram, 1)
	var tgPayload struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal([]byte(tgBodies[0]), &tgPayload); err != nil {
		t.Fatalf("unmarshal telegram payload: %v (%s)", err, tgBodies[0])
	}
	if tgPayload.ChatID != "-100" ||
		!strings.Contains(tgPayload.Text, "RLS · nginx-prod") ||
		!strings.Contains(tgPayload.Text, "sin comunicación") {
		t.Fatalf("telegram payload off: %+v", tgPayload)
	}

	// PUT con vacíos explícitos → canales deshabilitados (sin env detrás).
	if r := do(t, admin, http.MethodPut, base+"/api/v1/notify/settings",
		`{"discord_webhook_url":"","telegram_bot_token":"","telegram_chat_id":""}`); r.status != http.StatusOK {
		t.Fatalf("PUT clear: got %d %s", r.status, r.body)
	}
	m = get()
	if m["discord_enabled"] != false || m["telegram_enabled"] != false ||
		m["discord_webhook_url"] != nil || m["telegram_bot_token"] != nil || m["telegram_chat_id"] != nil {
		t.Fatalf("cleared settings: %+v", m)
	}

	// Nueva alerta (segundo agent en silencio) → NINGUNA llamada: canales apagados.
	callsD, _ := discord.state()
	callsT, _ := telegram.state()
	fireOfflineAlert(t, srv, st, broker, notifier, "rlst_notifset_off2", "2500200000000")
	time.Sleep(150 * time.Millisecond) // da una chance al (inexistente) send
	if c, _ := discord.state(); c != callsD {
		t.Fatalf("discord llamó tras deshabilitar: %d → %d", callsD, c)
	}
	if c, _ := telegram.state(); c != callsT {
		t.Fatalf("telegram llamó tras deshabilitar: %d → %d", callsT, c)
	}
}

// TestE2E_NotifySettingsEnvFallback covers the documented precedence: DB
// row fields win per-field, and an EMPTY DB row falls back to the env vars
// (RLS_NOTIF_*): alert lands in the fake webhook and GET masks the env URL.
func TestE2E_NotifySettingsEnvFallback(t *testing.T) {
	discord, _, rt := newFakePair(t)
	env := notify.Config{DiscordWebhookURL: "https://discord.com/api/webhooks/000000000000000000/TEST-ENV-envenvno"}
	srv, st, broker, notifier := newNotifySettingsServer(t, env, rt)
	base := srv.URL
	admin := bootstrapAdmin(t, srv)

	var view map[string]any
	r := do(t, admin, http.MethodGet, base+"/api/v1/notify/settings", "")
	if r.status != http.StatusOK {
		t.Fatalf("GET settings env fallback: %d %s", r.status, r.body)
	}
	if err := json.Unmarshal([]byte(r.body), &view); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, r.body)
	}
	if got := view["discord_webhook_url"]; got != "envenvno" {
		t.Fatalf("env fallback mask: want last 8 envenvno, got %v", got)
	}
	if view["discord_enabled"] != true || view["telegram_bot_token"] != nil {
		t.Fatalf("env fallback view: %+v", view)
	}

	// DB vacío → el env no muerto: la alerta atraviesa el fake.
	fireOfflineAlert(t, srv, st, broker, notifier, "rlst_envfb_off", "2501000000000")
	bodies := awaitCalls(t, discord, 1)
	if !strings.Contains(bodies[0], "nginx-prod") {
		t.Fatalf("env fallback delivery: %s", bodies[0])
	}

	// Sin tocar nada vía PUT (cuerpo {}), el env sigue vigente (PUT parcial).
	r = do(t, admin, http.MethodPut, base+"/api/v1/notify/settings", `{}`)
	if r.status != http.StatusOK {
		t.Fatalf("empty PUT: %d %s", r.status, r.body)
	}
	if err := json.Unmarshal([]byte(r.body), &view); err != nil {
		t.Fatalf("unmarshal PUT: %v (%s)", err, r.body)
	}
	if got := view["discord_webhook_url"]; got != "envenvno" {
		t.Fatalf("put {} must not touch env-backed field: %v", got)
	}
}

// marshalNeed unmarshals→reserializes a map for compact plaintext scans.
func marshalNeed(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
