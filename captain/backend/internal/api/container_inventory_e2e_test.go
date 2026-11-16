package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rls-legacy/captain/backend/internal/store"
)

func stowawayBeat(name, containerID, engineID string, ts int64) string {
	body := map[string]any{
		"hostname":     "inventory-vps",
		"name":         name,
		"container_id": containerID,
		"ts":           ts,
		"interval":     10,
		"labels":       map[string]string{"rls_vps_id": engineID, "rls_vps_name": "inventory-vps"},
		"metrics":      map[string]float64{"cpu_pct": 1, "ram_used_mb": 4, "ram_limit_mb": 64},
	}
	encoded, _ := json.Marshal(body)
	return string(encoded)
}

func postInventory(t *testing.T, srv *httptest.Server, token, body string) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/agents/inventory", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new inventory request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post inventory: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read inventory response: %v", err)
	}
	return response{status: resp.StatusCode, body: string(data)}
}

func TestE2E_ContainerInventoryKeepsRemovedInstancesAndAlerts(t *testing.T) {
	srv, st, _ := newTask4Server(t)
	admin := bootstrapAdmin(t, srv)
	_, token := createVPSTestToken(t, admin, srv.URL, "inventory-vps")

	if result := beat(t, srv, token, stowawayBeat("web", "container-web-old", "engine-inventory", validBeatTS)); result.status != http.StatusOK {
		t.Fatalf("first container heartbeat: %d %s", result.status, result.body)
	}
	if result := beat(t, srv, token, stowawayBeat("worker", "container-worker", "engine-inventory", validBeatTS+10_000)); result.status != http.StatusOK {
		t.Fatalf("second container heartbeat: %d %s", result.status, result.body)
	}

	if result := postInventory(t, srv, token,
		`{"engine_id":"engine-inventory","containers":[{"id":"container-web-old","state":"running"},{"id":"container-worker","state":"exited"}]}`); result.status != http.StatusOK {
		t.Fatalf("complete inventory was rejected: %d %s", result.status, result.body)
	}

	// The next complete snapshot proves the web container was removed. The
	// record stays in the fleet as missing and gets one permanent alert; a
	// repeated identical snapshot does not duplicate the alert.
	missingSnapshot := `{"engine_id":"engine-inventory","containers":[{"id":"container-worker","state":"exited"}]}`
	if result := postInventory(t, srv, token, missingSnapshot); result.status != http.StatusOK || !strings.Contains(result.body, `"changed":1`) {
		t.Fatalf("removed container was not reconciled: %d %s", result.status, result.body)
	}
	if result := postInventory(t, srv, token, missingSnapshot); result.status != http.StatusOK || !strings.Contains(result.body, `"changed":0`) {
		t.Fatalf("same snapshot should be idempotent: %d %s", result.status, result.body)
	}

	details, err := st.ListAgentsDetailed(t.Context(), false)
	if err != nil || len(details) != 2 {
		t.Fatalf("historical agents were not retained: rows=%d err=%v", len(details), err)
	}
	var oldAgentID int64
	for _, agent := range details {
		if agent.ContainerID.String == "container-web-old" {
			oldAgentID = agent.ID
			if agent.Status != "missing" || agent.OpenAlerts != 1 {
				t.Fatalf("removed container status/alert wrong: %+v", agent)
			}
		}
	}
	if oldAgentID == 0 {
		t.Fatal("missing container row was not retained")
	}
	alerts, err := st.ListAlerts(t.Context(), oldAgentID)
	if err != nil || len(alerts) != 1 || alerts[0].Type != "container_missing" || alerts[0].ResolvedAt.Valid {
		t.Fatalf("missing alert history wrong: %+v err=%v", alerts, err)
	}

	// Dokploy can replace a Compose replica using the same service name. The
	// new Docker ID becomes a new row; the old ID remains as a missing record.
	if result := beat(t, srv, token, stowawayBeat("web", "container-web-new", "engine-inventory", validBeatTS+20_000)); result.status != http.StatusOK {
		t.Fatalf("replacement container heartbeat: %d %s", result.status, result.body)
	}
	details, err = st.ListAgentsDetailed(t.Context(), false)
	if err != nil || len(details) != 3 {
		t.Fatalf("replacement must preserve prior instance: rows=%d err=%v", len(details), err)
	}
	newFound := false
	oldStillMissing := false
	for _, agent := range details {
		if agent.ContainerID.String == "container-web-new" {
			newFound = agent.Status == "online"
		}
		if agent.ContainerID.String == "container-web-old" {
			oldStillMissing = agent.Status == "missing"
		}
	}
	if !newFound || !oldStillMissing {
		t.Fatalf("replacement/history state mismatch: %+v", details)
	}

	// When the exact old Docker instance reappears, its missing alert resolves
	// automatically and the same historical record returns to online.
	if result := beat(t, srv, token, stowawayBeat("web", "container-web-old", "engine-inventory", validBeatTS+30_000)); result.status != http.StatusOK {
		t.Fatalf("old container heartbeat recovery: %d %s", result.status, result.body)
	}
	oldAgent, err := st.AgentByID(t.Context(), oldAgentID)
	if err != nil || oldAgent.Status != "online" {
		t.Fatalf("old instance did not recover: %+v err=%v", oldAgent, err)
	}
	alerts, err = st.ListAlerts(t.Context(), oldAgentID)
	if err != nil || len(alerts) != 1 || !alerts[0].ResolvedAt.Valid {
		t.Fatalf("missing alert was not resolved on recovery: %+v err=%v", alerts, err)
	}
}

func TestE2E_RemovedContainerNotifiesAndRecoveryResolves(t *testing.T) {
	webhook := &notifWebhook{statuses: []int{http.StatusOK}}
	fake := webhook.server(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "missing-notification-e2e.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	srv, _, _ := newNotifServer(t, st, fake.URL)
	admin := bootstrapAdmin(t, srv)
	_, token := createVPSTestToken(t, admin, srv.URL, "inventory-notification-vps")

	if result := beat(t, srv, token, stowawayBeat("api", "container-api", "engine-notification", validBeatTS)); result.status != http.StatusOK {
		t.Fatalf("container heartbeat: %d %s", result.status, result.body)
	}
	if result := postInventory(t, srv, token, `{"engine_id":"engine-notification","containers":[{"id":"container-api","state":"running"}]}`); result.status != http.StatusOK {
		t.Fatalf("initial inventory: %d %s", result.status, result.body)
	}
	if result := postInventory(t, srv, token, `{"engine_id":"engine-notification","containers":[]}`); result.status != http.StatusOK {
		t.Fatalf("container removal inventory: %d %s", result.status, result.body)
	}

	bodies := awaitCalls(t, webhook, 1)
	fired := decodeEmbed(t, bodies[0])
	if fired.Title != "🛰 api — container missing" || fired.Description != "the container is no longer present in this VPS Docker inventory" {
		t.Fatalf("missing notification content: title=%q description=%q", fired.Title, fired.Description)
	}

	// A heartbeat from that same immutable Docker ID is the recovery signal.
	if result := beat(t, srv, token, stowawayBeat("api", "container-api", "engine-notification", validBeatTS+10_000)); result.status != http.StatusOK {
		t.Fatalf("container recovery heartbeat: %d %s", result.status, result.body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if calls, _ := webhook.state(); calls >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, bodies = webhook.state()
	if len(bodies) != 2 {
		t.Fatalf("want both missing and recovery notifications, got %d calls: %v", len(bodies), bodies)
	}
	recovered := decodeEmbed(t, bodies[1])
	if recovered.Title != "✅ api — container detected again" || recovered.Description != "the container is reporting again and is present in Docker" {
		t.Fatalf("recovery notification content: title=%q description=%q", recovered.Title, recovered.Description)
	}
}

func TestE2E_InventoryRequiresVPSIdentityAndArray(t *testing.T) {
	srv, _, _ := newTask4Server(t)
	admin := bootstrapAdmin(t, srv)
	_, token := createVPSTestToken(t, admin, srv.URL, "inventory-validation")

	if result := postInventory(t, srv, token, `{"engine_id":"engine-a"}`); result.status != http.StatusBadRequest {
		t.Fatalf("missing containers array must be rejected: %d %s", result.status, result.body)
	}
	if result := postInventory(t, srv, token, `{"engine_id":"engine-a","containers":[{"id":"x","state":"running"},{"id":"x","state":"exited"}]}`); result.status != http.StatusBadRequest {
		t.Fatalf("duplicate IDs must be rejected: %d %s", result.status, result.body)
	}
	if result := postInventory(t, srv, token, `{"engine_id":"engine-a","containers":[]}`); result.status != http.StatusOK {
		t.Fatalf("empty inventory must be valid: %d %s", result.status, result.body)
	}
	if len(task13ListAgents(t, admin, srv, "")) != 0 {
		t.Fatal("empty inventory should not create agents")
	}
}
