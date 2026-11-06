package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rls-legacy/captain/backend/internal/api"
	"github.com/rls-legacy/captain/backend/internal/ingest"
	"github.com/rls-legacy/captain/backend/internal/store"
)

type policySnapshot struct {
	Scope                   string `json:"scope"`
	ScopeID                 string `json:"scope_id"`
	NotificationsEnabled    bool   `json:"notifications_enabled"`
	ResourceAlertsEnabled   bool   `json:"resource_alerts_enabled"`
	VPSNotificationsEnabled bool   `json:"vps_notifications_enabled"`
	Customized              bool   `json:"customized"`
	Inherited               bool   `json:"inherited"`
	ParentScopeID           string `json:"parent_scope_id"`
}

// TestE2E_NotificationPolicyScopes exercises real heartbeat/API/SQLite/
// ticker/webhook paths with multiple containers on two VPSes that share the
// same hostname. CPU/RAM alerts are still visible in Captain but external
// delivery is opt-in; VPS and per-container mutes apply to both outage and
// recovery transitions. A container may opt into resource notices only while
// its VPS has not disabled all external notifications.
func TestE2E_NotificationPolicyScopes(t *testing.T) {
	webhook := &notifWebhook{statuses: []int{200}}
	fake := webhook.server(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "notify-policy-e2e.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	srv, broker, notifier := newNotifServer(t, st, fake.URL)

	if r := do(t, nil, http.MethodPost, srv.URL+"/api/v1/auth/bootstrap", `{"username":"admin","password":"admin-password-1"}`); r.status != http.StatusCreated && r.status != http.StatusOK {
		t.Fatalf("bootstrap admin: %d %s", r.status, r.body)
	}
	admin := clientLoggedIn(t, srv, "admin", "admin-password-1")
	if r := do(t, admin, http.MethodPost, srv.URL+"/api/v1/users", `{"username":"viewer","password":"viewer-password-1","role":"viewer"}`); r.status != http.StatusCreated {
		t.Fatalf("create viewer: %d %s", r.status, r.body)
	}
	viewer := clientLoggedIn(t, srv, "viewer", "viewer-password-1")
	if r := do(t, viewer, http.MethodGet, srv.URL+"/api/v1/notification-policies/vps/engine-A", ""); r.status != http.StatusForbidden {
		t.Fatalf("viewer read policy: got %d want 403", r.status)
	}

	type container struct {
		token, name, engine string
	}
	containers := []container{
		{token: "rlst_policy_a_api", name: "api-a", engine: "engine-A"},
		{token: "rlst_policy_a_worker", name: "worker-a", engine: "engine-A"},
		{token: "rlst_policy_b_api", name: "api-b", engine: "engine-B"},
		{token: "rlst_policy_b_worker", name: "worker-b", engine: "engine-B"},
		{token: "rlst_policy_b_muted", name: "muted-b", engine: "engine-B"},
	}
	ids := make(map[string]int64, len(containers))
	for i, c := range containers {
		seedToken(t, st, c.token)
		body := policyBeat(c.name, c.engine, 2_501_000_000_000+int64(i)*1000, 10)
		if got := beat(t, srv, c.token, body); got.status != http.StatusOK {
			t.Fatalf("initial heartbeat %s: %d %s", c.name, got.status, got.body)
		}
		agent, err := st.AgentByTokenHash(t.Context(), api.HashAgentToken(c.token))
		if err != nil {
			t.Fatalf("lookup agent %s: %v", c.name, err)
		}
		ids[c.name] = agent.ID
	}

	// VPS A is muted as a whole. VPS B remains generally enabled, with
	// resource notices opted in only for api-b and all notices muted for one
	// specific container. New/unconfigured agents inherit the VPS default.
	putPolicy := func(path, body string) response {
		t.Helper()
		return do(t, admin, http.MethodPut, srv.URL+path, body)
	}
	if r := putPolicy("/api/v1/notification-policies/vps/engine-A", `{"notifications_enabled":false,"resource_alerts_enabled":true}`); r.status != http.StatusOK {
		t.Fatalf("mute VPS A: %d %s", r.status, r.body)
	}
	if r := putPolicy(fmt.Sprintf("/api/v1/notification-policies/agent/%d", ids["api-a"]), `{"notifications_enabled":true,"resource_alerts_enabled":true}`); r.status != http.StatusOK {
		t.Fatalf("container override must not bypass VPS mute: %d %s", r.status, r.body)
	}
	mutedPolicy := do(t, admin, http.MethodGet, fmt.Sprintf("%s/api/v1/notification-policies/agent/%d", srv.URL, ids["api-a"]), "")
	var mutedState policySnapshot
	if mutedPolicy.status != http.StatusOK || json.Unmarshal([]byte(mutedPolicy.body), &mutedState) != nil || mutedState.NotificationsEnabled || mutedState.VPSNotificationsEnabled {
		t.Fatalf("VPS mute must override a container opt-in: %d %s %+v", mutedPolicy.status, mutedPolicy.body, mutedState)
	}
	if r := putPolicy(fmt.Sprintf("/api/v1/notification-policies/agent/%d", ids["api-b"]), `{"notifications_enabled":true,"resource_alerts_enabled":true}`); r.status != http.StatusOK {
		t.Fatalf("opt in api-b resources: %d %s", r.status, r.body)
	}
	if r := putPolicy(fmt.Sprintf("/api/v1/notification-policies/agent/%d", ids["muted-b"]), `{"notifications_enabled":false,"resource_alerts_enabled":false}`); r.status != http.StatusOK {
		t.Fatalf("mute one container: %d %s", r.status, r.body)
	}
	if r := do(t, nil, http.MethodPut, srv.URL+"/api/v1/notification-policies/vps/engine-B", `{"notifications_enabled":false,"resource_alerts_enabled":false}`); r.status != http.StatusUnauthorized {
		t.Fatalf("anonymous policy write: got %d want 401", r.status)
	}

	// Each high-CPU transition creates an app alert. Only api-b is opted in
	// for external resource notifications; the other four stay silent.
	for i, c := range containers {
		body := policyBeat(c.name, c.engine, 2_501_100_000_000+int64(i)*1000, 99)
		if got := beat(t, srv, c.token, body); got.status != http.StatusOK {
			t.Fatalf("high CPU heartbeat %s: %d %s", c.name, got.status, got.body)
		}
	}
	bodies := awaitCalls(t, webhook, 1)
	if len(bodies) != 1 {
		t.Fatalf("resource policy delivered %d webhooks, want only api-b", len(bodies))
	}
	if title := decodeEmbed(t, bodies[0]).Title; !strings.Contains(title, "api-b") || !strings.Contains(title, "CPU") {
		t.Fatalf("wrong resource notification: %q", title)
	}

	alerts := do(t, admin, http.MethodGet, srv.URL+"/api/v1/alerts?scope=active", "")
	if alerts.status != http.StatusOK {
		t.Fatalf("list alerts: %d %s", alerts.status, alerts.body)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(alerts.body), &rows); err != nil {
		t.Fatalf("decode alerts: %v", err)
	}
	cpuAlerts := 0
	for _, row := range rows {
		if row["type"] == "high_cpu" {
			cpuAlerts++
		}
	}
	if cpuAlerts != len(containers) {
		t.Fatalf("resource alerts visible in Captain=%d, want all %d despite external mutes", cpuAlerts, len(containers))
	}

	// Simulate a VPS-wide outage with the real production ticker logic. VPS A
	// emits nothing, worker-b inherits default outage notifications, api-b is
	// individually enabled, and muted-b remains quiet.
	for _, id := range ids {
		if err := st.SetAgentLastSeen(t.Context(), id, time.Now().Add(-time.Hour).Unix()); err != nil {
			t.Fatalf("age last seen: %v", err)
		}
	}
	ticker := ingest.NewTicker(st, time.Hour, broker, notifier)
	marked, err := ticker.Tick(t.Context())
	if err != nil || marked != len(containers) {
		t.Fatalf("simulated VPS outage: marked=%d want=%d err=%v", marked, len(containers), err)
	}
	bodies = awaitCalls(t, webhook, 3) // api-b CPU + api-b/worker-b offline
	if len(bodies) != 3 {
		t.Fatalf("outage policy total deliveries=%d, want 3", len(bodies))
	}
	offlineNames := map[string]bool{}
	for _, body := range bodies[1:] {
		title := decodeEmbed(t, body).Title
		if !strings.Contains(title, "sin comunicación") {
			t.Fatalf("expected outage notification, got %q", title)
		}
		if strings.Contains(title, "api-b") {
			offlineNames["api-b"] = true
		}
		if strings.Contains(title, "worker-b") {
			offlineNames["worker-b"] = true
		}
		if strings.Contains(title, "api-a") || strings.Contains(title, "worker-a") || strings.Contains(title, "muted-b") {
			t.Fatalf("muted scope leaked an outage notification: %q", title)
		}
	}
	if !offlineNames["api-b"] || !offlineNames["worker-b"] {
		t.Fatalf("expected enabled B containers to notify, got %v", offlineNames)
	}

	// Recovery reports resolve open offline alerts automatically. Only the two
	// enabled B containers send recovery notifications; muted rows still
	// transition online and their alert history is resolved.
	for i, c := range containers {
		body := policyBeat(c.name, c.engine, 2_501_200_000_000+int64(i)*1000, 10)
		if got := beat(t, srv, c.token, body); got.status != http.StatusOK {
			t.Fatalf("recovery heartbeat %s: %d %s", c.name, got.status, got.body)
		}
	}
	bodies = awaitCalls(t, webhook, 5)
	if len(bodies) != 5 {
		t.Fatalf("recovery policy total deliveries=%d, want 5", len(bodies))
	}
	for _, body := range bodies[3:] {
		title := decodeEmbed(t, body).Title
		if !strings.Contains(title, "señal restablecida") || (!strings.Contains(title, "api-b") && !strings.Contains(title, "worker-b")) {
			t.Fatalf("unexpected recovery notification: %q", title)
		}
	}

	reset := do(t, admin, http.MethodDelete, fmt.Sprintf("%s/api/v1/notification-policies/agent/%d", srv.URL, ids["muted-b"]), "")
	if reset.status != http.StatusNoContent {
		t.Fatalf("reset container override: %d %s", reset.status, reset.body)
	}
	get := do(t, admin, http.MethodGet, fmt.Sprintf("%s/api/v1/notification-policies/agent/%d", srv.URL, ids["muted-b"]), "")
	var inherited policySnapshot
	if get.status != http.StatusOK || json.Unmarshal([]byte(get.body), &inherited) != nil || !inherited.Inherited || !inherited.NotificationsEnabled || inherited.ResourceAlertsEnabled {
		t.Fatalf("container should inherit VPS B defaults after reset: %d %s %+v", get.status, get.body, inherited)
	}
}

func policyBeat(name, engine string, tsMS int64, cpu float64) string {
	body := strings.Replace(validBeat(), `"hostname": "web-1"`, `"hostname": "ubuntu"`, 1)
	body = strings.Replace(body, `"name": "nginx-prod"`, `"name": "`+name+`"`, 1)
	body = strings.Replace(body, `"container_id": "abc123"`, `"container_id": "container-`+name+`"`, 1)
	body = strings.Replace(body, `"labels": {"app":"nginx","compose_project":"prod"}`, `"labels": {"app":"nginx","compose_project":"prod","rls_vps_id":"`+engine+`","rls_vps_name":"ubuntu"}`, 1)
	body = strings.Replace(body, "1700000000000", fmt.Sprint(tsMS), 1)
	body = strings.Replace(body, `"cpu_pct": 12.4`, fmt.Sprintf(`"cpu_pct": %.1f`, cpu), 1)
	return body
}

// TestE2E_NotificationPolicyRealSilenceDelay uses real wall-clock silence and
// the production ticker, but shortens only the sweep cadence. Each simulated
// agent reports every 5 s, so the same 3x grace period must not mark either
// container offline before 15 s; only the unmuted VPS should reach Discord.
func TestE2E_NotificationPolicyRealSilenceDelay(t *testing.T) {
	webhook := &notifWebhook{statuses: []int{200}}
	fake := webhook.server(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "notify-policy-delay.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	srv, broker, notifier := newNotifServer(t, st, fake.URL)

	const (
		tokenMuted = "rlst_policy_delay_muted"
		tokenOpen  = "rlst_policy_delay_open"
	)
	for i, item := range []struct{ token, name, engine string }{
		{tokenMuted, "service-muted", "engine-delay-A"},
		{tokenOpen, "service-open", "engine-delay-B"},
	} {
		seedToken(t, st, item.token)
		body := strings.Replace(policyBeat(item.name, item.engine, 2_501_300_000_000+int64(i)*1000, 10), `"interval": 10`, `"interval": 5`, 1)
		if got := beat(t, srv, item.token, body); got.status != http.StatusOK {
			t.Fatalf("initial heartbeat %s: %d %s", item.name, got.status, got.body)
		}
	}
	mutedAgent, err := st.AgentByTokenHash(t.Context(), api.HashAgentToken(tokenMuted))
	if err != nil {
		t.Fatalf("lookup muted agent: %v", err)
	}
	openAgent, err := st.AgentByTokenHash(t.Context(), api.HashAgentToken(tokenOpen))
	if err != nil {
		t.Fatalf("lookup enabled agent: %v", err)
	}
	if err := st.SetNotificationPolicy(t.Context(), store.NotificationScopeVPS, "engine-delay-A", false, false, time.Now().Unix()); err != nil {
		t.Fatalf("mute first VPS: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	defer func() {
		cancel()
		<-done
	}()
	go func() {
		defer close(done)
		ingest.NewTicker(st, 25*time.Millisecond, broker, notifier).Run(ctx)
	}()
	started := time.Now()
	deadline := started.Add(20 * time.Second)
	for time.Now().Before(deadline) {
		mutedAgent, err = st.AgentByID(t.Context(), mutedAgent.ID)
		if err != nil {
			cancel()
			t.Fatalf("refresh muted state: %v", err)
		}
		openAgent, err = st.AgentByID(t.Context(), openAgent.ID)
		if err != nil {
			cancel()
			t.Fatalf("refresh enabled state: %v", err)
		}
		if mutedAgent.Status == "offline" && openAgent.Status == "offline" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	elapsed := time.Since(started)
	if mutedAgent.Status != "offline" || openAgent.Status != "offline" {
		t.Fatalf("real silence timeout did not transition both agents by 20s: muted=%s open=%s elapsed=%s", mutedAgent.Status, openAgent.Status, elapsed)
	}
	if elapsed < 15*time.Second {
		t.Fatalf("agent went offline before 3x the 5s heartbeat interval: elapsed=%s", elapsed)
	}
	// Keep the policy-check context alive until the notifier has enqueued and
	// delivered the event. Cancelling immediately after observing the persisted
	// offline status can race the ticker's following ShouldNotifyAlert lookup.
	bodies := awaitCalls(t, webhook, 1)
	if len(bodies) != 1 || !strings.Contains(decodeEmbed(t, bodies[0]).Title, "service-open") {
		t.Fatalf("only the enabled VPS should notify after real silence: %#v", bodies)
	}
	cancel()
	<-done
}
