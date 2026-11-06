package api_test

// End-to-end tests for TASK-011 (DEC-011) over a real HTTP server backed by
// a SQLite file:
//
// create VPS token WITH tags (trim/dedupe/charset validation) → GET /tokens
// exposes the FULL plaintext + tags → its first beat adopts an agent and
// copies tags at adoption → PUT /tokens/{id}/tags updates the token template
// without rewriting adopted agents → DELETE (revoke) auto-dismisses its
// adopted container(s) (dismissed_at stamped, alerts resolved,
// agent hidden from the default list) → a beat with the SAME revoked token
// is 401 (does not revive anything) → a NEW token reusing one tag yields a
// NEW agent sharing the ecosystem tag (demostrable e2e).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rls-legacy/captain/backend/internal/ingest"
)

// task11Token mirrors the create/list token contract.
type task11Token struct {
	ID         int64    `json:"id"`
	Token      *string  `json:"token"`
	Name       string   `json:"name"`
	Tags       []string `json:"tags"`
	AgentID    *int64   `json:"agent_id"`
	Status     string   `json:"status"`
	Enrollment bool     `json:"enrollment"`
	VPSScoped  bool     `json:"vps_scoped"`
}

// task11AgentEntry is the agent-list projection slice needed here.
type task11AgentEntry struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Tags        []string       `json:"tags"`
	Labels      map[string]any `json:"labels"`
	DismissedAt *int64         `json:"dismissed_at"`
}

// TestE2E_TokenTagsEcosystem — the DEC-011 flow end to end.
func TestE2E_TokenTagsEcosystem(t *testing.T) {
	srv, st, _ := newTask4Server(t)
	admin := bootstrapAdmin(t, srv)
	ctx := t.Context()

	// ---- 1. create WITH tags (dedupe happens on the server) -------------
	create := func(body string) response {
		return do(t, admin, http.MethodPost, srv.URL+"/api/v1/tokens", body)
	}
	r := create(`{"name":"lab-api","tags":[" lab ","infra","lab"]}`)
	if r.status != http.StatusCreated {
		t.Fatalf("create token: got %d %s", r.status, r.body)
	}
	var created task11Token
	if err := json.Unmarshal([]byte(r.body), &created); err != nil {
		t.Fatalf("create body: %v (%s)", err, r.body)
	}
	if created.Token == nil || !strings.HasPrefix(*created.Token, "rlst_") || !created.Enrollment || !created.VPSScoped {
		t.Fatalf("create token plaintext: %+v", created)
	}
	if len(created.Tags) != 2 || created.Tags[0] != "lab" || created.Tags[1] != "infra" {
		t.Fatalf("tags not trimmed/deduped: %v", created.Tags)
	}
	plainToken := *created.Token

	// Negative validation batch: uppercase, bad charset, >24 chars, >5
	// tags, empty-after-trim array.
	for i, bad := range []string{
		`{"name":"x","tags":["Lab"]}`,
		`{"name":"x","tags":["a b"]}`,
		`{"name":"x","tags":["` + strings.Repeat("l", 25) + `"]}`,
		`{"name":"x","tags":["a","b","c","d","e","f"]}`,
		`{"name":"x","tags":["   "]}`,
	} {
		if rr := create(bad); rr.status != http.StatusBadRequest {
			t.Fatalf("bad tags[%d] %s: got %d %s", i, bad, rr.status, rr.body)
		}
	}

	// ---- 2. GET /tokens: full plaintext + tags (always visible/copyable) -
	r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/tokens", "")
	if r.status != http.StatusOK || !strings.Contains(r.body, plainToken) {
		t.Fatalf("token list missing plaintext: %d %s", r.status, r.body)
	}
	var list []task11Token
	if err := json.Unmarshal([]byte(r.body), &list); err != nil {
		t.Fatalf("list tokens: %v (%s)", err, r.body)
	}
	if len(list) != 1 || list[0].ID != created.ID || list[0].Token == nil ||
		*list[0].Token != plainToken || len(list[0].Tags) != 2 ||
		list[0].AgentID != nil || list[0].Status != "active" {
		t.Fatalf("token list wrong: %+v", list)
	}

	// ---- 3. first beat → container adopted AND tags copied --------------
	if b := beat(t, srv, plainToken, withVPSID(validBeat(), "engine-task11-a")); b.status != http.StatusOK {
		t.Fatalf("first beat: %d %s", b.status, b.body)
	}
	r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/agents", "")
	if r.status != http.StatusOK {
		t.Fatalf("agents list: %d %s", r.status, r.body)
	}
	var agents []task11AgentEntry
	if err := json.Unmarshal([]byte(r.body), &agents); err != nil || len(agents) != 1 {
		t.Fatalf("agents list: %v (%s)", err, r.body)
	}
	agentID := agents[0].ID
	if len(agents[0].Tags) != 2 || agents[0].Tags[0] != "lab" || agents[0].Tags[1] != "infra" {
		t.Fatalf("agent tags not copied from token: %v", agents[0].Tags)
	}
	if agents[0].DismissedAt != nil {
		t.Fatalf("fresh agent already dismissed: %+v", agents[0])
	}

	// Simulated silence: an offline alert is opened so step 5 can prove
	// the auto-dismiss RESOLVES it (same contract as DELETE /agents/{id},
	// TASK-009).
	if err := st.SetAgentLastSeen(ctx, agentID, time.Now().Add(-500*time.Second).Unix()); err != nil {
		t.Fatalf("backdate last_seen: %v", err)
	}
	tk := ingest.NewTicker(st, time.Hour)
	if marked, err := tk.Tick(ctx); err != nil || marked != 1 {
		t.Fatalf("offline tick: marked=%d err=%v", marked, err)
	}

	// ---- 4. PUT tags updates the template, not adopted containers --------
	r = do(t, admin, http.MethodPut, fmt.Sprintf("%s/api/v1/tokens/%d/tags", srv.URL, created.ID),
		`{"tags":["web","infra","prod"]}`)
	if r.status != http.StatusOK {
		t.Fatalf("put token tags: got %d %s", r.status, r.body)
	}
	if !strings.Contains(r.body, `"status":"active"`) {
		t.Fatalf("put tags did not return the token list: %s", r.body)
	}
	if err := json.Unmarshal([]byte(r.body), &list); err != nil || len(list) != 1 {
		t.Fatalf("put tags body: %v (%s)", err, r.body)
	}
	if len(list[0].Tags) != 3 || list[0].Tags[0] != "web" || list[0].Tags[2] != "prod" {
		t.Fatalf("token tags not updated: %v", list[0].Tags)
	}
	r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/agents", "")
	agents = nil
	if err := json.Unmarshal([]byte(r.body), &agents); err != nil {
		t.Fatalf("agents list: %v", err)
	}
	if len(agents) != 1 || len(agents[0].Tags) != 2 || agents[0].Tags[0] != "lab" || agents[0].Tags[1] != "infra" {
		t.Fatalf("updating token template must not rewrite an adopted VPS container: %v", agents)
	}
	// PUT validation: unknown token 404, bad charset 400, anonymous 401,
	// viewer 403.
	if r = do(t, admin, http.MethodPut, srv.URL+"/api/v1/tokens/9999/tags", `{"tags":["x"]}`); r.status != http.StatusNotFound {
		t.Fatalf("put tags unknown token: got %d", r.status)
	}
	if r = do(t, admin, http.MethodPut, fmt.Sprintf("%s/api/v1/tokens/%d/tags", srv.URL, created.ID), `{"tags":["UPPER"]}`); r.status != http.StatusBadRequest {
		t.Fatalf("put tags bad charset: got %d %s", r.status, r.body)
	}
	if r = do(t, nil, http.MethodPut, fmt.Sprintf("%s/api/v1/tokens/%d/tags", srv.URL, created.ID), `{"tags":["x"]}`); r.status != http.StatusUnauthorized {
		t.Fatalf("put tags anon: got %d", r.status)
	}

	// ---- 5. revoke → token revoked AND linked agent auto-dismissed ------
	r = do(t, admin, http.MethodDelete, fmt.Sprintf("%s/api/v1/tokens/%d", srv.URL, created.ID), "")
	if r.status != http.StatusNoContent {
		t.Fatalf("revoke: got %d %s", r.status, r.body)
	}
	// Token listed as revoked (plaintext still there).
	r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/tokens", "")
	if r.status != http.StatusOK || !strings.Contains(r.body, `"status":"revoked"`) || !strings.Contains(r.body, plainToken) {
		t.Fatalf("token not revoked in list: %s", r.body)
	}
	// Default agent view hides the auto-dismissed agent; scope=all shows
	// it with dismissed_at stamped.
	r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/agents", "")
	if err := json.Unmarshal([]byte(r.body), &agents); err != nil || len(agents) != 0 {
		t.Fatalf("dismissed agent still in default list: %v (%s)", agents, r.body)
	}
	r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/agents?scope=all", "")
	if err := json.Unmarshal([]byte(r.body), &agents); err != nil || len(agents) != 1 || agents[0].DismissedAt == nil {
		t.Fatalf("dismissed agent missing from scope=all: %v (%s)", agents, r.body)
	}
	if len(agents[0].Tags) != 2 || agents[0].Tags[0] != "lab" || agents[0].Tags[1] != "infra" {
		t.Fatalf("dismissed container should retain its adoption-time tags: %+v", agents[0])
	}
	// Direct store check: all its alerts resolved after the auto-dismiss.
	agentAlerts, err := st.ListAlerts(ctx, agentID)
	if err != nil {
		t.Fatalf("alerts lookup: %v", err)
	}
	for _, al := range agentAlerts {
		if !al.ResolvedAt.Valid {
			t.Fatalf("auto-dismiss left an open alert: %+v", al)
		}
	}
	// Idempotent re-revoke: still 204, agent stays dismissed (no 2nd time).
	if r = do(t, admin, http.MethodDelete, fmt.Sprintf("%s/api/v1/tokens/%d", srv.URL, created.ID), ""); r.status != http.StatusNoContent {
		t.Fatalf("re-revoke: got %d", r.status)
	}

	// ---- 6. the same revoked token can NOT revive the agent -------------
	lateBeat := `{"hostname":"web-1","ts":` + strconv.FormatInt(validBeatTS+60_000, 10) + `,
	  "metrics":{"cpu_pct":5,"ram_used_mb":10,"ram_limit_mb":512}}`
	if b := beat(t, srv, plainToken, lateBeat); b.status != http.StatusUnauthorized {
		t.Fatalf("revoked token beat: got %d %s", b.status, b.body)
	}
	// …and the agent row still exists as the permanent record.
	if _, err := st.AgentByID(ctx, agentID); err != nil {
		t.Fatalf("revocation deleted the agent record: %v", err)
	}

	// ---- 7. NEW VPS token reusing a tag → NEW agent, SAME ecosystem tag -
	r = create(`{"name":"lab-api-v2","tags":["infra"]}`)
	if r.status != http.StatusCreated {
		t.Fatalf("create token v2: got %d %s", r.status, r.body)
	}
	var created2 task11Token
	if err := json.Unmarshal([]byte(r.body), &created2); err != nil {
		t.Fatalf("create v2 body: %v", err)
	}
	if created2.Token == nil {
		t.Fatalf("create v2 missing plaintext")
	}
	v2 := *created2.Token
	differentBeat := strings.Replace(validBeat(), "nginx-prod", "nginx-canary", 1)
	differentBeat = strings.Replace(differentBeat, strconv.FormatInt(validBeatTS, 10), strconv.FormatInt(validBeatTS+120_000, 10), 1)
	if b := beat(t, srv, v2, withVPSID(differentBeat, "engine-task11-v2")); b.status != http.StatusOK {
		t.Fatalf("v2 first beat: %d %s", b.status, b.body)
	}
	r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/agents", "")
	if err := json.Unmarshal([]byte(r.body), &agents); err != nil || len(agents) != 1 {
		t.Fatalf("default agents list after v2: %v (%s)", agents, r.body)
	}
	if agents[0].ID == agentID {
		t.Fatalf("v2 beat revived the old agent instead of registering a new one")
	}
	if len(agents[0].Tags) != 1 || agents[0].Tags[0] != "infra" {
		t.Fatalf("v2 agent tags wrong: %v", agents[0].Tags)
	}
	// Two distinct tokens now share the "infra" ecosystem tag.
	r = do(t, admin, http.MethodGet, srv.URL+"/api/v1/tokens", "")
	if err := json.Unmarshal([]byte(r.body), &list); err != nil || len(list) != 2 {
		t.Fatalf("tokens list after v2: %v (%s)", list, r.body)
	}
	tagged := 0
	for _, tk := range list {
		for _, g := range tk.Tags {
			if g == "infra" {
				tagged++
				break
			}
		}
	}
	if tagged != 2 {
		t.Fatalf("ecosystem tag shared by %d tokens, want 2: %+v", tagged, list)
	}

	// ---- 8. viewer cannot manage tokens --------------------------------
	if r := do(t, admin, http.MethodPost, srv.URL+"/api/v1/users",
		`{"username":"viewer11","password":"viewer-password-1","role":"viewer"}`); r.status != http.StatusCreated {
		t.Fatalf("create viewer: %d %s", r.status, r.body)
	}
	viewer := clientLoggedIn(t, srv, "viewer11", "viewer-password-1")
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/tokens", ""},
		{http.MethodPost, "/api/v1/tokens", `{"name":"hax"}`},
		{http.MethodPut, "/api/v1/tokens/1/tags", `{"tags":["x"]}`},
		{http.MethodDelete, "/api/v1/tokens/1", ""},
	} {
		if rr := do(t, viewer, tc.method, srv.URL+tc.path, tc.body); rr.status != http.StatusForbidden {
			t.Fatalf("viewer %s %s: got %d %s", tc.method, tc.path, rr.status, rr.body)
		}
	}
}
