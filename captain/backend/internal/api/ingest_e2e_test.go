package api_test

// End-to-end test of the ingest flow (TASK-003) over a real HTTP server
// backed by a SQLite file: token inserted directly in the DB (plaintext is
// never stored — only its SHA-256 hex), then heartbeat happy path, auth
// failures, offline detection via a manual ticker tick and resurrection
// with alert resolution, plus retry dedupe (INSERT OR REPLACE on the
// (agent_id, ts) primary key).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rls-legacy/captain/backend/internal/api"
	"github.com/rls-legacy/captain/backend/internal/ingest"
	"github.com/rls-legacy/captain/backend/internal/store"
)

func newIngestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ingest-test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	srv := httptest.NewServer(api.New(st, []byte("ingest-e2e-secret")).Handler())
	t.Cleanup(srv.Close)
	return srv, st
}

// seedToken inserts a token row (hash AND plaintext persisted, DEC-011;
// tags unset).
func seedToken(t *testing.T, st *store.Store, plaintext string) int64 {
	t.Helper()
	id, err := st.CreateToken(t.Context(), api.HashAgentToken(plaintext), plaintext, "e2e-token", "")
	if err != nil {
		t.Fatalf("seed token: %v", err)
	}
	return id
}

// beat POSTs a heartbeat with the given bearer token and body.
func beat(t *testing.T, srv *httptest.Server, token, body string) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/agents/heartbeat", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new heartbeat request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read heartbeat body: %v", err)
	}
	return response{status: resp.StatusCode, body: string(data)}
}

// validBeat is the canonical payload: fixed ts so every assertion on the
// metrics row count is deterministic.
const validBeatTS = int64(1700000000000)

func validBeat() string {
	return `{
	  "hostname": "web-1", "ts": ` + strconv.FormatInt(validBeatTS, 10) + `, "interval": 10,
	  "name": "nginx-prod", "container_id": "abc123",
	  "labels": {"app":"nginx","compose_project":"prod"},
	  "metrics": {"cpu_pct": 12.4, "ram_used_mb": 145.2, "ram_limit_mb": 512,
	              "rx_mb": 12.9, "tx_mb": 4.2, "uptime_sec": 8472}
	}`
}

// withVPSID upgrades a legacy-shaped fixture to the Stowaway heartbeat
// contract used by tokens minted through the new per-VPS API.
func withVPSID(body, engineID string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		panic(err)
	}
	labels, _ := payload["labels"].(map[string]any)
	if labels == nil {
		labels = make(map[string]any)
	}
	labels["rls_vps_id"] = engineID
	payload["labels"] = labels
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// metricsTSFor converts a payload unix_ms ts the same way the handler does.
func metricsTSFor(tsMS int64) int64 { return tsMS / 1000 }

func requireHeartbeatOK(t *testing.T, r response, wantInterval int) {
	t.Helper()
	if r.status != http.StatusOK {
		t.Fatalf("heartbeat: got %d %s", r.status, r.body)
	}
	var resp struct {
		Status       string `json:"status"`
		NextInterval int    `json:"next_interval"`
	}
	if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
		t.Fatalf("unmarshal heartbeat response %s: %v", r.body, err)
	}
	if resp.Status != "ok" || resp.NextInterval != wantInterval {
		t.Fatalf("heartbeat response: %+v", resp)
	}
}

func TestE2E_HeartbeatFlow(t *testing.T) {
	srv, st := newIngestServer(t)
	ctx := t.Context()

	seedToken(t, st, "rlst_e2e_valid")
	good := "rlst_e2e_valid"
	goodHash := api.HashAgentToken(good)

	// 1. Invalid token → 401 with {"error":...}.
	r := beat(t, srv, "rlst_totally_wrong", validBeat())
	if r.status != http.StatusUnauthorized || !strings.Contains(r.body, `"error"`) {
		t.Fatalf("bad token heartbeat: got %d %s", r.status, r.body)
	}

	// 2. Missing Authorization header → 401.
	r = beat(t, srv, "", validBeat())
	if r.status != http.StatusUnauthorized {
		t.Fatalf("no auth heartbeat: got %d %s", r.status, r.body)
	}

	// 3. Missing body / non-numeric metric → 400.
	if r = beat(t, srv, good, ""); r.status != http.StatusBadRequest {
		t.Fatalf("empty body: got %d %s", r.status, r.body)
	}
	r = beat(t, srv, good, `{"hostname":"x","ts":1,"interval":10,"metrics":{"cpu_pct":"oops"}}`)
	if r.status != http.StatusBadRequest {
		t.Fatalf("non-numeric metric: got %d %s", r.status, r.body)
	}

	// 4. First valid heartbeat → auto-register, online, metric stored with
	// the payload ts (DSG-003).
	requireHeartbeatOK(t, beat(t, srv, good, validBeat()), 10)

	agent, err := st.AgentByTokenHash(ctx, goodHash)
	if err != nil {
		t.Fatalf("agent lookup: %v", err)
	}
	if agent.Status != "online" || agent.Name != "nginx-prod" || agent.Hostname != "web-1" {
		t.Fatalf("agent flags: %+v", agent)
	}
	if agent.ContainerID.String != "abc123" {
		t.Fatalf("container_id: %+v", agent)
	}
	// Metric stored under ts from the payload (1700000000000ms → 1700000000s).
	n, err := st.CountMetricsForAgent(ctx, agent.ID)
	if err != nil || n != 1 {
		t.Fatalf("metrics count: n=%d err=%v", n, err)
	}
	// Token row got back-linked to its 1:1 agent.
	tok, err := st.TokenActiveByHash(ctx, goodHash)
	if err != nil || !tok.AgentID.Valid || tok.AgentID.Int64 != agent.ID {
		t.Fatalf("token agent backlink wrong: %+v err=%v", tok, err)
	}

	// 5. Out-of-range interval 999 → next_interval falls back to 10; in-range
	// 100 passes through. Distinct ts values keep the row count predictable.
	r = beat(t, srv, good, `{"hostname":"web-1","ts":1700000001000,"interval":999,"metrics":{"cpu_pct":1}}`)
	requireHeartbeatOK(t, r, 10)
	r = beat(t, srv, good, `{"hostname":"web-1","ts":1700000002000,"interval":100,"metrics":{"cpu_pct":1}}`)
	requireHeartbeatOK(t, r, 100)
	if n, _ := st.CountMetricsForAgent(ctx, agent.ID); n != 3 {
		t.Fatalf("expected 3 metric rows after interval checks, got %d", n)
	}

	// 6. Offline simulation: backdate last_seen beyond 3×interval (interval
	// is now 100 from beat #3, so 3×100=300s; age 500s is enough) and run
	// the ticker manually (Tick is exposed for exactly this).
	if err := st.SetAgentLastSeen(ctx, agent.ID, time.Now().Add(-500*time.Second).Unix()); err != nil {
		t.Fatalf("backdate last_seen: %v", err)
	}
	tk := ingest.NewTicker(st, time.Hour) // interval never fires on its own here
	marked, err := tk.Tick(ctx)
	if err != nil || marked != 1 {
		t.Fatalf("tick: marked=%d err=%v", marked, err)
	}
	offlineAgent, _ := st.AgentByTokenHash(ctx, goodHash)
	if offlineAgent.Status != "offline" {
		t.Fatalf("agent not offline after tick: %+v", offlineAgent)
	}
	alerts, err := st.ListAlerts(ctx, offlineAgent.ID)
	if err != nil || len(alerts) != 1 || alerts[0].Type != "offline" ||
		alerts[0].ResolvedAt.Valid || alerts[0].Value.Float64 <= 300 {
		t.Fatalf("offline alert wrong: %+v err=%v", alerts, err)
	}
	// Second tick while offline: nothing left online, no duplicate alert.
	if marked, err = tk.Tick(ctx); err != nil || marked != 0 {
		t.Fatalf("second tick: marked=%d err=%v", marked, err)
	}
	if alerts2, _ := st.ListAlerts(ctx, offlineAgent.ID); len(alerts2) != 1 {
		t.Fatalf("duplicate offline alert: %d rows", len(alerts2))
	}

	// 7. Resurrection: next heartbeat flips the agent online and resolves
	// the offline alert.
	revived := `{"hostname":"web-1","ts":1700000005000,"name":"nginx-prod",
	  "metrics":{"cpu_pct":13.1,"ram_used_mb":150.1,"ram_limit_mb":512,"rx_mb":13,"tx_mb":4.4,"uptime_sec":8520}}`
	requireHeartbeatOK(t, beat(t, srv, good, revived), 10)
	revivedAgent, _ := st.AgentByTokenHash(ctx, goodHash)
	if revivedAgent.Status != "online" {
		t.Fatalf("agent not online after revive: %+v", revivedAgent)
	}
	alerts, err = st.ListAlerts(ctx, revivedAgent.ID)
	if err != nil || len(alerts) != 1 || !alerts[0].ResolvedAt.Valid {
		t.Fatalf("offline alert not resolved: %+v err=%v", alerts, err)
	}
	// Four samples now: 1700000000/01/02 + 1700000005.
	if n, _ := st.CountMetricsForAgent(ctx, revivedAgent.ID); n != 4 {
		t.Fatalf("expected 4 metric rows after revive, got %d", n)
	}

	// 8. Retry with the identical ts must not duplicate (INSERT OR REPLACE
	// on PK (agent_id, ts)).
	requireHeartbeatOK(t, beat(t, srv, good, revived), 10)
	if n, _ := st.CountMetricsForAgent(ctx, revivedAgent.ID); n != 4 {
		t.Fatalf("retry duplicated metrics: got %d rows", n)
	}

	// 9. Revoked token → 401 even though the agent row still exists.
	if err := st.RevokeToken(ctx, tok.ID); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	r = beat(t, srv, good, validBeat())
	if r.status != http.StatusUnauthorized {
		t.Fatalf("revoked token heartbeat: got %d %s", r.status, r.body)
	}
}
