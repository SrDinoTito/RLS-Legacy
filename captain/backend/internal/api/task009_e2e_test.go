package api_test

// End-to-end test of the agent soft-dismiss lifecycle (TASK-009) over a
// real HTTP server backed by a SQLite file:
//
// token+beat → agent auto-registers → offline tick opens an alert →
// DELETE /agents/{id} (204; idempotent 204; 404 unknown; 400 bad id;
// viewer 403) → the open alert is RESOLVED with " — agent dado de baja"
// appended (user requirement: baja soft PERO con registro) →
// GET /agents default hides the dismissed agent; ?scope=all lists it with
// dismissed_at set; unknown scope 400 → ticker skips dismissed agents (no
// offline alert) → heartbeat restores the agent (dismissed_at NULL, back in
// the default list) → the ordinary ticker cycle works for it again
// (offline alert fires, next beat resolves it).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rls-legacy/captain/backend/internal/api"
	"github.com/rls-legacy/captain/backend/internal/ingest"
	"github.com/rls-legacy/captain/backend/internal/store"
)

// newTask009Server boots the API with a fresh SQLite file and creates the
// first admin through the public bootstrap endpoint (201 + session cookie).
func newTask009Server(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "task009-test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	srv := httptest.NewServer(api.New(st, []byte("task009-e2e-secret")).Handler())
	t.Cleanup(srv.Close)

	r := do(t, nil, http.MethodPost, srv.URL+"/api/v1/auth/bootstrap",
		`{"username":"admin","password":"task009-admin-1"}`)
	if r.status != http.StatusCreated {
		t.Fatalf("bootstrap: got %d %s", r.status, r.body)
	}
	return srv, st
}

// agentList unwraps a GET /api/v1/agents response body.
type agentListEntry struct {
	ID          int64  `json:"id"`
	Status      string `json:"status"`
	DismissedAt *int64 `json:"dismissed_at"`
}

func fetchAgents(t *testing.T, srv *httptest.Server, cl *http.Client, scope string) []agentListEntry {
	t.Helper()
	url := srv.URL + "/api/v1/agents"
	if scope != "" {
		url += "?scope=" + scope
	}
	r := do(t, cl, http.MethodGet, url, "")
	if r.status != http.StatusOK {
		t.Fatalf("GET /agents?scope=%s: got %d %s", scope, r.status, r.body)
	}
	var out []agentListEntry
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatalf("unmarshal agent list %s: %v", r.body, err)
	}
	return out
}

func TestE2E_AgentDismissLifecycle(t *testing.T) {
	srv, st := newTask009Server(t)
	ctx := t.Context()

	// 1. Token + first beat → agent auto-registers (1:1 token↔agent).
	seedToken(t, st, "rlst_task009")
	tok := "rlst_task009"
	requireHeartbeatOK(t, beat(t, srv, tok, validBeat()), 10)
	agent, err := st.AgentByTokenHash(ctx, api.HashAgentToken(tok))
	if err != nil {
		t.Fatalf("agent lookup: %v", err)
	}

	// 2. Silence > 3×interval + manual tick → open offline alert.
	if err := st.SetAgentLastSeen(ctx, agent.ID, time.Now().Add(-500*time.Second).Unix()); err != nil {
		t.Fatalf("backdate last_seen: %v", err)
	}
	tk := ingest.NewTicker(st, time.Hour) // never fires on its own; Tick only
	if marked, err := tk.Tick(ctx); err != nil || marked != 1 {
		t.Fatalf("offline tick: marked=%d err=%v", marked, err)
	}
	alerts, err := st.ListAlerts(ctx, agent.ID)
	if err != nil || len(alerts) != 1 || alerts[0].Type != "offline" || alerts[0].ResolvedAt.Valid {
		t.Fatalf("expected 1 open offline alert: %+v err=%v", alerts, err)
	}
	openAlertID := alerts[0].ID

	// 3. Admin + viewer sessions for the permission checks.
	admin := clientLoggedIn(t, srv, "admin", "task009-admin-1")
	if r := do(t, admin, http.MethodPost, srv.URL+"/api/v1/users",
		`{"username":"viewer9","password":"viewer-password-1","role":"viewer"}`); r.status != http.StatusCreated {
		t.Fatalf("create viewer: got %d %s", r.status, r.body)
	}
	viewer := clientLoggedIn(t, srv, "viewer9", "viewer-password-1")

	// 4. DELETE /agents/{id}: viewer 403, admin 204, idempotent 204,
	// unknown 404, invalid id 400 (admin-only endpoint).
	del := func(cl *http.Client, id string) response {
		return do(t, cl, http.MethodDelete, fmt.Sprintf("%s/api/v1/agents/%s", srv.URL, id), "")
	}
	if r := del(viewer, fmt.Sprintf("%d", agent.ID)); r.status != http.StatusForbidden {
		t.Fatalf("viewer delete agent: got %d %s", r.status, r.body)
	}
	if r := del(admin, fmt.Sprintf("%d", agent.ID)); r.status != http.StatusNoContent {
		t.Fatalf("delete agent: got %d %s", r.status, r.body)
	}
	if r := del(admin, fmt.Sprintf("%d", agent.ID)); r.status != http.StatusNoContent {
		t.Fatalf("delete agent twice (idempotent): got %d %s", r.status, r.body)
	}
	if r := del(admin, "999"); r.status != http.StatusNotFound {
		t.Fatalf("delete unknown agent: got %d %s", r.status, r.body)
	}
	if r := del(admin, "abc"); r.status != http.StatusBadRequest {
		t.Fatalf("delete invalid id: got %d %s", r.status, r.body)
	}

	// 5. SOFT dismissal: row retained, dismissed_at stamped, the open
	// alert is resolved and its message records the dismissal.
	dismissed, err := st.AgentByTokenHash(ctx, api.HashAgentToken(tok))
	if err != nil || !dismissed.DismissedAt.Valid {
		t.Fatalf("dismissed_at not set: %+v err=%v", dismissed, err)
	}
	alerts, err = st.ListAlerts(ctx, agent.ID)
	if err != nil || len(alerts) != 1 {
		t.Fatalf("alerts after dismiss: %+v err=%v", alerts, err)
	}
	if alerts[0].ID != openAlertID || !alerts[0].ResolvedAt.Valid {
		t.Fatalf("open alert not resolved by dismiss: %+v", alerts[0])
	}
	if !strings.Contains(alerts[0].Message.String, "agent dado de baja") {
		t.Fatalf("dismissed alert message lacks record: %q", alerts[0].Message.String)
	}

	// 6. Default list hides the dismissed agent; scope=all shows it with
	// dismissed_at; unknown scope → 400.
	active := fetchAgents(t, srv, admin, "")
	if len(active) != 0 {
		t.Fatalf("default list should hide dismissed agent, got %+v", active)
	}
	all := fetchAgents(t, srv, admin, "all")
	if len(all) != 1 || all[0].ID != agent.ID || all[0].DismissedAt == nil {
		t.Fatalf("scope=all should list dismissed agent: %+v", all)
	}
	if r := do(t, admin, http.MethodGet, srv.URL+"/api/v1/agents?scope=bogus", ""); r.status != http.StatusBadRequest {
		t.Fatalf("unknown scope: got %d %s", r.status, r.body)
	}
	if r := do(t, admin, http.MethodGet, srv.URL+"/api/v1/agents", ""); r.status != http.StatusOK {
		t.Fatalf("default scope still 200: got %d", r.status)
	}

	// 7. The offline ticker must SKIP dismissed agents even when they look
	// stale: force the row online + stale, tick → 0 marks, no new alert.
	if err := st.SetAgentStatus(ctx, agent.ID, "online"); err != nil {
		t.Fatalf("restore status fixture: %v", err)
	}
	if err := st.SetAgentLastSeen(ctx, agent.ID, time.Now().Add(-500*time.Second).Unix()); err != nil {
		t.Fatalf("backdate last_seen again: %v", err)
	}
	if marked, err := tk.Tick(ctx); err != nil || marked != 0 {
		t.Fatalf("dismissed stale agent must be skipped: marked=%d err=%v", marked, err)
	}
	if alerts, _ = st.ListAlerts(ctx, agent.ID); len(alerts) != 1 {
		t.Fatalf("dismissed agent must not fire offline alerts: %+v", alerts)
	}

	// 8. Heartbeat of the same token restores the agent: dismissed_at
	// cleared, back in the default list.
	staleBeat := `{"hostname":"web-1","ts":1700000009000,"name":"nginx-prod","interval":10,
	  "metrics":{"cpu_pct":14.0,"ram_used_mb":146.0,"ram_limit_mb":512,"rx_mb":13,"tx_mb":4.3,"uptime_sec":8500}}`
	requireHeartbeatOK(t, beat(t, srv, tok, staleBeat), 10)
	restored, err := st.AgentByTokenHash(ctx, api.HashAgentToken(tok))
	if err != nil {
		t.Fatalf("restored lookup: %v", err)
	}
	if restored.DismissedAt.Valid {
		t.Fatalf("dismissed_at should be cleared after heartbeat: %+v", restored)
	}
	if restored.Status != "online" {
		t.Fatalf("restored agent not online: %+v", restored)
	}
	fresh := fetchAgents(t, srv, admin, "")
	if len(fresh) != 1 || fresh[0].ID != agent.ID || fresh[0].DismissedAt != nil {
		t.Fatalf("restored agent must be in the default list: %+v", fresh)
	}

	// 9. Ordinary ticker works for it again: stale + tick → offline with a
	// NEW open offline alert, and the next beat resolves it.
	if err := st.SetAgentLastSeen(ctx, agent.ID, time.Now().Add(-500*time.Second).Unix()); err != nil {
		t.Fatalf("backdate last_seen (normal cycle): %v", err)
	}
	if marked, err := tk.Tick(ctx); err != nil || marked != 1 {
		t.Fatalf("live agent must go offline: marked=%d err=%v", marked, err)
	}
	alerts, err = st.ListAlerts(ctx, agent.ID)
	if err != nil || len(alerts) != 2 {
		t.Fatalf("expected a new offline alert on top of the dismissed one: %+v err=%v", alerts, err)
	}
	var newAlert store.Alert
	for _, a := range alerts {
		if a.ID != openAlertID {
			newAlert = a
		}
	}
	if newAlert.ResolvedAt.Valid || newAlert.Message.String != "sin comunicación (posible caída)" {
		t.Fatalf("new offline alert wrong: %+v", newAlert)
	}

	// 10. Revive beat → the new alert is resolved; the dismissal record of
	// the FIRST alert stays untouched (both still listed: permanent record).
	requireHeartbeatOK(t, beat(t, srv, tok, staleBeat), 10)
	alerts, err = st.ListAlerts(ctx, agent.ID)
	if err != nil || len(alerts) != 2 {
		t.Fatalf("record view keeps both alerts: %+v err=%v", alerts, err)
	}
	for _, a := range alerts {
		if !a.ResolvedAt.Valid {
			t.Fatalf("alert %d still open after revival", a.ID)
		}
	}
	if !strings.Contains(alerts[0].Message.String, "agent dado de baja") &&
		!strings.Contains(alerts[1].Message.String, "agent dado de baja") {
		t.Fatalf("dismissal record lost: %+v", alerts)
	}
}
