package api_test

// End-to-end tests for TASK-004: token CRUD (since TASK-011/DEC-011 the
// plaintext is persisted and exposed by the list — no more one-time show), agents
// list with latest metrics + labels as raw JSON, history rollup, thresholds
// GET/PUT with validation, sustained threshold alerts with dedupe, manual
// resolution, and the SSE event stream fed by the real transitions.

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rls-legacy/captain/backend/internal/api"
	"github.com/rls-legacy/captain/backend/internal/ingest"
	"github.com/rls-legacy/captain/backend/internal/sse"
	"github.com/rls-legacy/captain/backend/internal/store"
)

// newTask4Server builds an e2e server returning every dependency the
// TASK-004 flows need: the HTTP server, the store (to seed/redate rows)
// and the broker (so ticker and API broadcast on the same instance).
func newTask4Server(t *testing.T) (*httptest.Server, *store.Store, *sse.Broker) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "task4.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	broker := sse.New()
	srv := httptest.NewServer(api.New(st, []byte("task4-secret"), broker).Handler())
	t.Cleanup(srv.Close)
	return srv, st, broker
}

// bootstrapAdmin creates the first admin and returns a cookie-bearing
// client acting as him.
func bootstrapAdmin(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	r := do(t, clientFor(), http.MethodPost, srv.URL+"/api/v1/auth/bootstrap",
		`{"username":"admin","password":"admin-password-1"}`)
	if r.status != http.StatusCreated {
		t.Fatalf("bootstrap: got %d %s", r.status, r.body)
	}
	return clientLoggedIn(t, srv, "admin", "admin-password-1")
}

// streamingAdmin logs in and returns a cookie-bearing client WITHOUT a
// total timeout (needed for an SSE request that must stay open).
func streamingAdmin(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("jar: %v", err)
	}
	cl := &http.Client{Jar: jar}
	r := do(t, cl, http.MethodPost, srv.URL+"/api/v1/auth/login",
		`{"username":"admin","password":"admin-password-1"}`)
	if r.status != http.StatusOK {
		t.Fatalf("login for stream: %d %s", r.status, r.body)
	}
	return cl
}

func TestE2E_MigrationIsIdempotent(t *testing.T) {
	// The uptime_sec ALTER runs on every startup: a fresh database gains
	// the column, a reopened (already migrated) one must not fail on the
	// duplicate-column error.
	dir := t.TempDir()
	path := filepath.Join(dir, "reopen.db")

	st1, err := store.Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	// Sanity: the column exists — seed an agent and a metric with uptime.
	if _, err := st1.CreateAgent(t.Context(), "hash-open-1", "seed", "h", "c1", "", 10, 1000); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if err := st1.InsertMetric(t.Context(), &store.Metric{
		AgentID: 1, TS: 1000,
		CPUPct:    sql.NullFloat64{Float64: 1, Valid: true},
		UptimeSec: sql.NullFloat64{Float64: 42, Valid: true},
	}); err != nil {
		t.Fatalf("insert with uptime: %v", err)
	}
	st1.Close()

	st2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen (duplicate-column path): %v", err)
	}
	defer st2.Close()
	if _, err := st2.HistoryRollup(t.Context(), 1, 0, 2000, 60); err != nil {
		t.Fatalf("rollup after reopen: %v", err)
	}
}

// ---------------------------------------------------------------- tokens --

func TestE2E_TokenCRUDFlow(t *testing.T) {
	srv, st, _ := newTask4Server(t)
	admin := bootstrapAdmin(t, srv)
	ctx := t.Context()

	// 1. Create → 201 with token rlst_<32 hex> as a VPS enrollment credential.
	// TASK-011/DEC-011: the
	// plaintext is PERSISTED (tokens.token_plain) so the UI can show/copy
	// it at any time — no longer "shown only once".
	r := do(t, admin, http.MethodPost, srv.URL+"/api/v1/tokens", `{"name":"web-2"}`)
	if r.status != http.StatusCreated {
		t.Fatalf("create token: got %d %s", r.status, r.body)
	}
	var created struct {
		ID         int64    `json:"id"`
		Name       string   `json:"name"`
		Token      string   `json:"token"`
		Tags       []string `json:"tags"`
		Enrollment bool     `json:"enrollment"`
		VPSScoped  bool     `json:"vps_scoped"`
	}
	if err := json.Unmarshal([]byte(r.body), &created); err != nil {
		t.Fatalf("create token body: %v (%s)", err, r.body)
	}
	if !strings.HasPrefix(created.Token, "rlst_") || len(created.Token) != len("rlst_")+32 {
		t.Fatalf("token format: %q", created.Token)
	}
	if created.Name != "web-2" || created.ID == 0 || !created.Enrollment || !created.VPSScoped {
		t.Fatalf("create token response: %+v", created)
	}
	// The plaintext is stored hash+plain: hash is the credential's use
	// path, token_plain feeds the always-visible UI.
	tokRow, err := st.TokenByID(ctx, created.ID)
	if err != nil || tokRow.TokenHash != api.HashAgentToken(created.Token) || !tokRow.Enrollment || !tokRow.VPSScoped {
		t.Fatalf("token hash wrong: %+v err=%v", tokRow, err)
	}

	// 2. List shows metadata, status active, the FULL plaintext (DEC-011)
	// and tags — NO generated-hash echo is ever serialized.
	r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/tokens", "")
	if r.status != http.StatusOK {
		t.Fatalf("list tokens: got %d %s", r.status, r.body)
	}
	if !strings.Contains(r.body, created.Token) || strings.Contains(strings.ToLower(r.body), "hash") {
		t.Fatalf("token list must expose the plaintext: %s", r.body)
	}
	var list []struct {
		ID        int64    `json:"id"`
		Token     string   `json:"token"`
		Name      string   `json:"name"`
		Tags      []string `json:"tags"`
		AgentID   *int64   `json:"agent_id"`
		CreatedAt int64    `json:"created_at"`
		RevokedAt *int64   `json:"revoked_at"`
		Status    string   `json:"status"`
		VPSScoped bool     `json:"vps_scoped"`
	}
	if err := json.Unmarshal([]byte(r.body), &list); err != nil {
		t.Fatalf("list tokens body: %v (%s)", err, r.body)
	}
	if len(list) != 1 || list[0].ID != created.ID || list[0].Status != "active" ||
		list[0].AgentID != nil || list[0].Token != created.Token || !list[0].VPSScoped ||
		len(list[0].Tags) != 0 {
		t.Fatalf("token list wrong: %+v", list)
	}

	// 3. Empty name → 400.
	if r = do(t, admin, http.MethodPost, srv.URL+"/api/v1/tokens", `{"name":"  "}`); r.status != http.StatusBadRequest {
		t.Fatalf("empty name: got %d %s", r.status, r.body)
	}

	// 4. The plaintext is a real working VPS credential: the first heartbeat
	// adopts a container; enrollment keeps agent_id null and tracks 1:N links.
	if b := beat(t, srv, created.Token, withVPSID(validBeat(), "engine-task004")); b.status != http.StatusOK {
		t.Fatalf("heartbeat with new token: got %d %s", b.status, b.body)
	}
	r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/tokens", "")
	var list2 []map[string]any
	if err := json.Unmarshal([]byte(r.body), &list2); err != nil ||
		len(list2) != 1 || list2[0]["agent_id"] != nil || list2[0]["adopted_count"] != float64(1) {
		t.Fatalf("VPS token did not adopt one container: %v (%s)", err, r.body)
	}
	r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/agents", "")
	var activeAgents []struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(r.body), &activeAgents); err != nil || len(activeAgents) != 1 {
		t.Fatalf("adopted container not listed: %v (%s)", err, r.body)
	}
	agentID := activeAgents[0].ID
	if _, err := st.AgentByID(ctx, agentID); err != nil {
		t.Fatalf("agent bound to token missing: %v", err)
	}

	// 5. Delete (revoke) → 204. Heartbeat afterwards → 401; the agent row
	// survives the revocation (a token revocation never deletes an agent).
	r = do(t, admin, http.MethodDelete, fmt.Sprintf("%s/api/v1/tokens/%d", srv.URL, created.ID), "")
	if r.status != http.StatusNoContent {
		t.Fatalf("revoke: got %d %s", r.status, r.body)
	}
	if b := beat(t, srv, created.Token, validBeat()); b.status != http.StatusUnauthorized {
		t.Fatalf("revoked token heartbeat: got %d %s", b.status, b.body)
	}
	if _, err := st.AgentByID(ctx, agentID); err != nil {
		t.Fatalf("revocation deleted the agent: %v", err)
	}

	// 6. Idempotent re-delete → still 204; unknown id → 404.
	if r = do(t, admin, http.MethodDelete, fmt.Sprintf("%s/api/v1/tokens/%d", srv.URL, created.ID), ""); r.status != http.StatusNoContent {
		t.Fatalf("re-revoke: got %d", r.status)
	}
	if r = do(t, admin, http.MethodDelete, srv.URL+"/api/v1/tokens/9999", ""); r.status != http.StatusNotFound {
		t.Fatalf("unknown token delete: got %d", r.status)
	}

	// 7. Listed status now revoked.
	if r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/tokens", ""); strings.Contains(r.body, `"status":"active"`) || !strings.Contains(r.body, `"status":"revoked"`) {
		t.Fatalf("token status not revoked after delete: %s", r.body)
	}

	// 8. Viewers cannot manage tokens.
	if r = do(t, admin, http.MethodPost, srv.URL+"/api/v1/users",
		`{"username":"viewer2","password":"viewer-password-1","role":"viewer"}`); r.status != http.StatusCreated {
		t.Fatalf("create viewer: %d %s", r.status, r.body)
	}
	viewer := clientLoggedIn(t, srv, "viewer2", "viewer-password-1")
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/tokens", ""},
		{http.MethodPost, "/api/v1/tokens", `{"name":"hax"}`},
		{http.MethodDelete, "/api/v1/tokens/1", ""},
	} {
		if r = do(t, viewer, tc.method, srv.URL+tc.path, tc.body); r.status != http.StatusForbidden {
			t.Fatalf("viewer %s %s: got %d %s", tc.method, tc.path, r.status, r.body)
		}
	}
	_ = tokRow
}

// ------------------------------------------- list / history / thresholds --

type task4Agent struct {
	ID            int64          `json:"id"`
	Name          string         `json:"name"`
	Hostname      string         `json:"hostname"`
	ContainerID   *string        `json:"container_id"`
	Status        string         `json:"status"`
	Interval      int            `json:"interval"`
	Labels        map[string]any `json:"labels"`
	ThresholdsCPU float64        `json:"thresholds_cpu"`
	ThresholdsRAM float64        `json:"thresholds_ram"`
	LastSeen      any            `json:"last_seen"`
	Metrics       *struct {
		TS         int64   `json:"ts"`
		CPUPct     float64 `json:"cpu_pct"`
		RamUsedMB  float64 `json:"ram_used_mb"`
		RamLimitMB float64 `json:"ram_limit_mb"`
		UptimeSec  float64 `json:"uptime_sec"`
	} `json:"metrics"`
	AlertCount int64 `json:"alert_count"`
}

type historyDTO struct {
	AgentID int64            `json:"agent_id"`
	Step    int64            `json:"step"`
	Points  []task4HistPoint `json:"points"`
}

type task4HistPoint struct {
	TS         int64    `json:"ts"`
	CPUPct     *float64 `json:"cpu_pct"`
	RamUsedMB  *float64 `json:"ram_used_mb"`
	RamLimitMB *float64 `json:"ram_limit_mb"`
	RxMB       *float64 `json:"rx_mb"`
	TxMB       *float64 `json:"tx_mb"`
	UptimeSec  *float64 `json:"uptime_sec"`
}

func closeTo(a, b float64) bool {
	return a > b-1e-3 && a < b+1e-3
}

func floatAt(p *float64, name string, t *testing.T) float64 {
	t.Helper()
	if p == nil {
		t.Fatalf("%s missing (null) in point", name)
	}
	return *p
}

func TestE2E_AgentsHistoryThresholds(t *testing.T) {
	srv, st, _ := newTask4Server(t)
	admin := bootstrapAdmin(t, srv)
	ctx := t.Context()

	seedToken(t, st, "rlst_task4_hist")
	// Canonical first beat: ts → 1700000000s, labels JSON, uptime 8472.
	if b := beat(t, srv, "rlst_task4_hist", validBeat()); b.status != http.StatusOK {
		t.Fatalf("beat 1: %d %s", b.status, b.body)
	}

	r := do(t, admin, http.MethodGet, srv.URL+"/api/v1/agents", "")
	if r.status != http.StatusOK {
		t.Fatalf("list agents: %d %s", r.status, r.body)
	}
	var agents []task4Agent
	if err := json.Unmarshal([]byte(r.body), &agents); err != nil || len(agents) != 1 {
		t.Fatalf("agents list: err=%v n=%d body=%s", err, len(agents), r.body)
	}
	a := agents[0]
	// Labels arrive as a real JSON object (unstringified, not a string).
	if a.Labels["app"] != "nginx" || a.Labels["compose_project"] != "prod" {
		t.Fatalf("labels not a JSON object: %v", a.Labels)
	}
	if a.Metrics == nil || a.Metrics.CPUPct != 12.4 || a.Metrics.UptimeSec != 8472 ||
		a.Metrics.TS != 1700000000 || a.Metrics.RamUsedMB != 145.2 || a.Metrics.RamLimitMB != 512 {
		t.Fatalf("latest metrics wrong: %+v", a.Metrics)
	}
	if a.Status != "online" || a.AlertCount != 0 || a.ThresholdsCPU != 90 || a.ThresholdsRAM != 95 {
		t.Fatalf("agent row wrong: %+v", a)
	}
	if a.ContainerID == nil || *a.ContainerID != "abc123" {
		t.Fatalf("container_id wrong: %+v", a.ContainerID)
	}
	if a.Name != "nginx-prod" || a.Hostname != "web-1" || a.Interval != 10 || a.LastSeen == nil {
		t.Fatalf("identity fields wrong: %+v", a)
	}

	// Extra samples: 2 more in step-60-bucket misalignment… the beats are:
	// ts 1700000010 (cpu 50, ram 200/512) and 1700000020 (cpu 80,
	// ram 220/512); one new sample 1700000100 (cpu 20, NO ram limit →
	// carry-forward) and one fact-resolvable high sample later.
	nfv := func(v float64) sql.NullFloat64 { return sql.NullFloat64{Float64: v, Valid: v > 0} }
	insert := func(ts int64, cpu, ramUsed, ramLimit, rx, tx, uptime float64) {
		t.Helper()
		if err := st.InsertMetric(ctx, &store.Metric{
			AgentID:    a.ID,
			TS:         ts,
			CPUPct:     nfv(cpu),
			RamUsedMB:  nfv(ramUsed),
			RamLimitMB: nfv(ramLimit),
			RxMB:       nfv(rx),
			TxMB:       nfv(tx),
			UptimeSec:  nfv(uptime),
		}); err != nil {
			t.Fatalf("insert metric ts %d: %v", ts, err)
		}
	}
	insert(1700000010, 50, 200, 512, 20, 8, 8482)
	insert(1700000020, 80, 220, 512, 25, 9, 8492)
	insert(1700000100, 20, 180, 0, 12, 5, 8572)

	// History, default step 60: buckets (ts/60)*60 → 1699999980 (3 samples)
	// and 1700000100 (1 sample).
	u := fmt.Sprintf("%s/api/v1/agents/%d/history?from=1700000000&to=1700000200", srv.URL, a.ID)
	var hist historyDTO
	if r = do(t, admin, http.MethodGet, u, ""); r.status != http.StatusOK {
		t.Fatalf("history: %d %s", r.status, r.body)
	}
	if err := json.Unmarshal([]byte(r.body), &hist); err != nil {
		t.Fatalf("history body: %v (%s)", err, r.body)
	}
	if hist.AgentID != a.ID || hist.Step != 60 || len(hist.Points) != 2 ||
		hist.Points[0].TS != 1699999980 || hist.Points[1].TS != 1700000100 {
		t.Fatalf("history shape wrong: %+v", hist)
	}
	p0, p1 := hist.Points[0], hist.Points[1]
	// Bucket 0: AVG cpu over the 3 samples; MAX ram_used 220; MAX rx 25;
	// MAX uptime 8492; ram_limit 512 (its own bucket data).
	if !closeTo(floatAt(p0.CPUPct, "cpu", t), (12.4+50+80)/3) ||
		!closeTo(floatAt(p0.RamUsedMB, "ram_used", t), 220) ||
		!closeTo(floatAt(p0.RamLimitMB, "ram_limit", t), 512) ||
		!closeTo(floatAt(p0.RxMB, "rx", t), 25) ||
		!closeTo(floatAt(p0.UptimeSec, "uptime", t), 8492) {
		t.Fatalf("bucket 0 aggregation wrong: %+v", p0)
	}
	// Bucket 1: cpu 20, NO ram_limit of its own → carry-forward 512.
	if !closeTo(floatAt(p1.CPUPct, "cpu", t), 20) ||
		!closeTo(floatAt(p1.RamLimitMB, "ram_limit carry", t), 512) ||
		!closeTo(floatAt(p1.UptimeSec, "uptime", t), 8572) {
		t.Fatalf("bucket 1 aggregation/carry wrong: %+v", p1)
	}

	// step=30: sample 1700000000 → bucket 1699999980; samples 10&20 →
	// bucket 1700000010 (AVG (50+80)/2=65); 1700000100 → bucket 1700000100.
	u = fmt.Sprintf("%s/api/v1/agents/%d/history?from=1700000000&to=1700000200&step=30", srv.URL, a.ID)
	var hist2 historyDTO
	if r = do(t, admin, http.MethodGet, u, ""); r.status != http.StatusOK {
		t.Fatalf("history step=30: %d %s", r.status, r.body)
	}
	if err := json.Unmarshal([]byte(r.body), &hist2); err != nil ||
		hist2.Step != 30 || len(hist2.Points) != 3 ||
		hist2.Points[0].TS != 1699999980 || hist2.Points[1].TS != 1700000010 ||
		hist2.Points[2].TS != 1700000100 {
		t.Fatalf("step 30 buckets wrong: %v (%s)", err, r.body)
	}
	if !closeTo(floatAt(hist2.Points[0].CPUPct, "cpu", t), 12.4) ||
		!closeTo(floatAt(hist2.Points[1].CPUPct, "cpu avg", t), (50+80)/2) ||
		!closeTo(floatAt(hist2.Points[2].CPUPct, "cpu", t), 20) {
		t.Fatalf("step 30 aggregation wrong: %+v", hist2.Points)
	}

	// Malformed params → 400 (batch).
	for _, bad := range []string{
		"from=abc&to=100",
		"from=100&to=",
		"from=100&to=src",
		"from=200&to=100", // from > to
		"from=100&to=110&step=abc",
		"from=100&to=110&step=0",
		"from=100&to=110&step=-5",
	} {
		u = fmt.Sprintf("%s/api/v1/agents/%d/history?%s", srv.URL, a.ID, bad)
		if r = do(t, admin, http.MethodGet, u, ""); r.status != http.StatusBadRequest {
			t.Fatalf("history %q: want 400 got %d %s", bad, r.status, r.body)
		}
	}
	// Unknown agent → 404; anonymous → 401.
	u = srv.URL + "/api/v1/agents/999/history?from=100&to=200"
	if r = do(t, admin, http.MethodGet, u, ""); r.status != http.StatusNotFound {
		t.Fatalf("history unknown agent: got %d", r.status)
	}
	if r = do(t, nil, http.MethodGet, srv.URL+"/api/v1/agents", ""); r.status != http.StatusUnauthorized {
		t.Fatalf("anon agents list: got %d", r.status)
	}

	// ---- Thresholds GET/PUT --------------------------------------------
	u = fmt.Sprintf("%s/api/v1/agents/%d/thresholds", srv.URL, a.ID)
	var thr struct {
		ThresholdsCPU float64 `json:"thresholds_cpu"`
		ThresholdsRAM float64 `json:"thresholds_ram"`
	}
	if r = do(t, admin, http.MethodGet, u, ""); r.status != http.StatusOK {
		t.Fatalf("get thresholds: %d %s", r.status, r.body)
	}
	if err := json.Unmarshal([]byte(r.body), &thr); err != nil ||
		thr.ThresholdsCPU != 90 || thr.ThresholdsRAM != 95 {
		t.Fatalf("default thresholds wrong: %s", r.body)
	}
	if r = do(t, admin, http.MethodPut, u, `{"thresholds_cpu":85,"thresholds_ram":80}`); r.status != http.StatusOK {
		t.Fatalf("put thresholds: %d %s", r.status, r.body)
	}
	if err := json.Unmarshal([]byte(r.body), &thr); err != nil ||
		thr.ThresholdsCPU != 85 || thr.ThresholdsRAM != 80 {
		t.Fatalf("put response wrong: %s", r.body)
	}
	// GET confirms persistence.
	if r = do(t, admin, http.MethodGet, u, ""); !strings.Contains(r.body, `85`) || !strings.Contains(r.body, `80`) {
		t.Fatalf("thresholds not persisted: %s", r.body)
	}
	// Validation: cpu [10,100], ram [50,100]; missing fields.
	for _, bad := range []string{
		`{"thresholds_cpu":5,"thresholds_ram":80}`,
		`{"thresholds_cpu":150,"thresholds_ram":80}`,
		`{"thresholds_cpu":85,"thresholds_ram":30}`,
		`{"thresholds_cpu":85,"thresholds_ram":500}`,
		`{"thresholds_ram":80}`,
	} {
		if r = do(t, admin, http.MethodPut, u, bad); r.status != http.StatusBadRequest {
			t.Fatalf("bad thresholds %s: got %d %s", bad, r.status, r.body)
		}
	}
	// Unknown agent → 404 (GET and PUT).
	if r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/agents/999/thresholds", ""); r.status != http.StatusNotFound {
		t.Fatalf("thresholds unknown agent: got %d", r.status)
	}
	if r = do(t, admin, http.MethodPut, srv.URL+"/api/v1/agents/999/thresholds",
		`{"thresholds_cpu":85,"thresholds_ram":80}`); r.status != http.StatusNotFound {
		t.Fatalf("put thresholds unknown agent: got %d", r.status)
	}
}

// ------------------------------------------ threshold alerts + resolve --

func TestE2E_ThresholdAlertsAndResolution(t *testing.T) {
	srv, st, _ := newTask4Server(t)
	admin := bootstrapAdmin(t, srv)
	ctx := t.Context()

	seedToken(t, st, "rlst_task4_alert")
	if b := beat(t, srv, "rlst_task4_alert", strings.Replace(validBeat(), "1700000000000", "1800000000000", 1)); b.status != http.StatusOK {
		t.Fatalf("beat: %d %s", b.status, b.body)
	}
	r := do(t, admin, http.MethodGet, srv.URL+"/api/v1/agents", "")
	var agents []task4Agent
	if err := json.Unmarshal([]byte(r.body), &agents); err != nil || len(agents) != 1 {
		t.Fatalf("agents list: %v (%s)", err, r.body)
	}
	agentID := agents[0].ID

	// Tighten the RAM threshold to 80%.
	if r = do(t, admin, http.MethodPut,
		fmt.Sprintf("%s/api/v1/agents/%d/thresholds", srv.URL, agentID),
		`{"thresholds_cpu":90,"thresholds_ram":80}`); r.status != http.StatusOK {
		t.Fatalf("put thresholds: %d %s", r.status, r.body)
	}

	// High-RAM beat: 480/512 = 93.75% > 80 → one high_ram alert.
	ramBeat := `{"hostname":"web-1","ts":1800000050000,
	  "metrics":{"cpu_pct":10,"ram_used_mb":480,"ram_limit_mb":512,"rx_mb":1,"tx_mb":1,"uptime_sec":9000}}`
	if b := beat(t, srv, "rlst_task4_alert", ramBeat); b.status != http.StatusOK {
		t.Fatalf("ram beat: %d %s", b.status, b.body)
	}
	var alerts []struct {
		ID         int64    `json:"id"`
		AgentID    int64    `json:"agent_id"`
		AgentName  *string  `json:"agent_name"`
		Type       string   `json:"type"`
		Value      *float64 `json:"value"`
		ResolvedAt *int64   `json:"resolved_at"`
	}
	if r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/alerts", ""); r.status != http.StatusOK {
		t.Fatalf("alerts: %d %s", r.status, r.body)
	}
	if err := json.Unmarshal([]byte(r.body), &alerts); err != nil || len(alerts) != 1 {
		t.Fatalf("alerts list: %v (%s)", err, r.body)
	}
	al := alerts[0]
	if al.Type != "high_ram" || al.AgentID != agentID || al.ResolvedAt != nil ||
		al.Value == nil || !closeTo(*al.Value, 93.75) ||
		al.AgentName == nil || *al.AgentName != "nginx-prod" {
		t.Fatalf("high_ram alert wrong: %+v", al)
	}

	// Second breach later must NOT stack another alert (dedupe).
	if b := beat(t, srv, "rlst_task4_alert", strings.Replace(ramBeat, "1800000050000", "1800000100000", 1)); b.status != http.StatusOK {
		t.Fatalf("ram beat 2: %d %s", b.status, b.body)
	}
	r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/alerts", "")
	var deduped []map[string]any
	_ = json.Unmarshal([]byte(r.body), &deduped)
	if len(deduped) != 1 {
		t.Fatalf("high_ram dedupe failed: %d rows (%s)", len(deduped), r.body)
	}

	// High-CPU breach (cpu 95 > cpu threshold 90) → a second alert.
	cpuBeat := `{"hostname":"web-1","ts":1800000150000,
	  "metrics":{"cpu_pct":95,"ram_used_mb":100,"ram_limit_mb":512,"rx_mb":1,"tx_mb":1,"uptime_sec":9100}}`
	if b := beat(t, srv, "rlst_task4_alert", cpuBeat); b.status != http.StatusOK {
		t.Fatalf("cpu beat: %d %s", b.status, b.body)
	}
	r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/alerts", "")
	var two []map[string]any
	_ = json.Unmarshal([]byte(r.body), &two)
	if len(two) != 2 {
		t.Fatalf("high_cpu alert missing: (%s)", r.body)
	}
	// Agent name attached for the UI list.
	if r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/alerts", ""); !strings.Contains(r.body, `"agent_name":"nginx-prod"`) {
		t.Fatalf("agent name missing in alerts: %s", r.body)
	}

	// scope=weird → 400.
	if r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/alerts?scope=weird", ""); r.status != http.StatusBadRequest {
		t.Fatalf("bad scope: got %d", r.status)
	}

	// Resolve the high_ram alert: 200; re-resolve → 409; unknown → 404.
	var highRamID int64
	for _, al := range alerts {
		if al.Type == "high_ram" {
			highRamID = al.ID
		}
	}
	_ = al
	if r = do(t, admin, http.MethodPost, fmt.Sprintf("%s/api/v1/alerts/%d/resolve", srv.URL, highRamID), ""); r.status != http.StatusOK {
		t.Fatalf("resolve: %d %s", r.status, r.body)
	}
	if r = do(t, admin, http.MethodPost, fmt.Sprintf("%s/api/v1/alerts/%d/resolve", srv.URL, highRamID), ""); r.status != http.StatusConflict {
		t.Fatalf("re-resolve: got %d", r.status)
	}
	if r = do(t, admin, http.MethodPost, srv.URL+"/api/v1/alerts/4242/resolve", ""); r.status != http.StatusNotFound {
		t.Fatalf("resolve unknown: got %d", r.status)
	}
	// scope=active (default) no longer lists it; scope=all does.
	if r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/alerts", ""); strings.Contains(r.body, `"type":"high_ram"`) {
		t.Fatalf("resolved alert still active: %s", r.body)
	}
	if r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/alerts?scope=all", ""); !strings.Contains(r.body, `"type":"high_ram"`) {
		t.Fatalf("resolved alert missing from all: %s", r.body)
	}
	// Unauthenticated read/resolve → 401.
	if r = do(t, nil, http.MethodGet, srv.URL+"/api/v1/alerts", ""); r.status != http.StatusUnauthorized {
		t.Fatalf("anon alerts: got %d", r.status)
	}
	if r = do(t, nil, http.MethodPost, srv.URL+"/api/v1/alerts/1/resolve", ""); r.status != http.StatusUnauthorized {
		t.Fatalf("anon resolve: got %d", r.status)
	}
	_ = st
	_ = ctx
}

// ------------------------------------------------------------------- SSE --

// readFrame reads ONE SSE/HTTP frame (through \n\n) from the persistent
// reader with a timeout. Buffered reads ahead stay inside the same reader,
// so ordered sequential reads do not lose frames.
func readFrame(t *testing.T, br *bufio.Reader, timeout time.Duration) string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		var frame strings.Builder
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				ch <- "READ_ERR:" + err.Error()
				return
			}
			frame.WriteString(line)
			if line == "\n" {
				ch <- frame.String()
				return
			}
		}
	}()
	select {
	case f := <-ch:
		return f
	case <-time.After(timeout):
		return "TIMEOUT"
	}
}

func TestE2E_SSEStreamOfTransitions(t *testing.T) {
	srv, st, broker := newTask4Server(t)
	admin := bootstrapAdmin(t, srv)
	ctx := t.Context()

	streamAdmin := streamingAdmin(t, srv)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/stream", nil)
	if err != nil {
		t.Fatalf("new stream req: %v", err)
	}
	resp, err := streamAdmin.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK ||
		!strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") ||
		!strings.Contains(resp.Header.Get("Cache-Control"), "no-cache") {
		t.Fatalf("stream headers wrong: %d %q %q", resp.StatusCode,
			resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"))
	}
	br := bufio.NewReader(resp.Body)
	if f := readFrame(t, br, 5*time.Second); !strings.HasPrefix(f, ": connected") {
		t.Fatalf("expected connected comment, got %q", f)
	}

	seedToken(t, st, "rlst_task4_sse")
	// 1. First beat → event heartbeat (status online).
	if b := beat(t, srv, "rlst_task4_sse", strings.Replace(validBeat(), "1700000000000", "2110000000000", 1)); b.status != http.StatusOK {
		t.Fatalf("sse beat: %d %s", b.status, b.body)
	}
	if f := readFrame(t, br, 5*time.Second); !strings.HasPrefix(f, "event: heartbeat\n") ||
		!strings.Contains(f, `"status":"online"`) {
		t.Fatalf("expected heartbeat event, got %q", f)
	}

	// 2. Silence + tick → event agent_offline (broadcast by the ticker).
	agent, err := st.AgentByTokenHash(ctx, api.HashAgentToken("rlst_task4_sse"))
	if err != nil {
		t.Fatalf("agent lookup: %v", err)
	}
	if err := st.SetAgentLastSeen(ctx, agent.ID, time.Now().Add(-500*time.Second).Unix()); err != nil {
		t.Fatalf("redate: %v", err)
	}
	ticker := ingest.NewTicker(st, time.Hour, broker)
	marked, err := ticker.Tick(ctx)
	if err != nil || marked != 1 {
		t.Fatalf("tick: %v marked=%d", err, marked)
	}
	if f := readFrame(t, br, 5*time.Second); !strings.HasPrefix(f, "event: agent_offline\n") {
		t.Fatalf("expected agent_offline, got %q", f)
	}

	// 3. Resurrection → event agent_online, followed by alert_resolved (the
	// offline alert the beat just closed) and the beat's heartbeat.
	if b := beat(t, srv, "rlst_task4_sse", strings.Replace(validBeat(), "1700000000000", "2110000100000", 1)); b.status != http.StatusOK {
		t.Fatalf("revive beat: %d %s", b.status, b.body)
	}
	if f := readFrame(t, br, 5*time.Second); !strings.HasPrefix(f, "event: agent_online\n") {
		t.Fatalf("expected agent_online, got %q", f)
	}
	if f := readFrame(t, br, 5*time.Second); !strings.HasPrefix(f, "event: alert_resolved\n") {
		t.Fatalf("expected alert_resolved after online, got %q", f)
	}
	if f := readFrame(t, br, 5*time.Second); !strings.HasPrefix(f, "event: heartbeat\n") {
		t.Fatalf("expected heartbeat after online, got %q", f)
	}

	// 4. Threshold breach → event alert_new(high_ram), then its heartbeat.
	if r := do(t, admin, http.MethodPut,
		fmt.Sprintf("%s/api/v1/agents/%d/thresholds", srv.URL, agent.ID),
		`{"thresholds_cpu":90,"thresholds_ram":80}`); r.status != http.StatusOK {
		t.Fatalf("put thresholds: %d %s", r.status, r.body)
	}
	ramBeat := `{"hostname":"web-1","ts":2110000200000,
	  "metrics":{"cpu_pct":10,"ram_used_mb":480,"ram_limit_mb":512,"rx_mb":1,"tx_mb":1,"uptime_sec":9500}}`
	if b := beat(t, srv, "rlst_task4_sse", ramBeat); b.status != http.StatusOK {
		t.Fatalf("ram beat: %d %s", b.status, b.body)
	}
	if f := readFrame(t, br, 5*time.Second); !strings.HasPrefix(f, "event: alert_new\n") || !strings.Contains(f, "high_ram") {
		t.Fatalf("expected alert_new(high_ram), got %q", f)
	}
	if f := readFrame(t, br, 5*time.Second); !strings.HasPrefix(f, "event: heartbeat\n") {
		t.Fatalf("expected heartbeat after alert, got %q", f)
	}

	// 5. Manual resolve → event alert_resolved.
	open, err := st.ListAlertsAll(ctx, true)
	if err != nil || len(open) != 1 {
		t.Fatalf("open alerts lookup: %v", err)
	}
	if r := do(t, admin, http.MethodPost, fmt.Sprintf("%s/api/v1/alerts/%d/resolve", srv.URL, open[0].ID), ""); r.status != http.StatusOK {
		t.Fatalf("resolve: %d %s", r.status, r.body)
	}
	if f := readFrame(t, br, 5*time.Second); !strings.HasPrefix(f, "event: alert_resolved\n") {
		t.Fatalf("expected alert_resolved, got %q", f)
	}

	// 6. A direct broker broadcast also reaches the stream (unit-level).
	broker.Broadcast("agent_offline", map[string]any{"agent_id": agent.ID})
	if f := readFrame(t, br, 5*time.Second); !strings.HasPrefix(f, "event: agent_offline\n") {
		t.Fatalf("expected raw broker broadcast, got %q", f)
	}

	// 7. Unauthenticated stream → 401.
	if r := do(t, nil, http.MethodGet, srv.URL+"/api/v1/stream", ""); r.status != http.StatusUnauthorized {
		t.Fatalf("anon stream: got %d", r.status)
	}
}
