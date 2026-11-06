package api_test

// End-to-end tests for TASK-013 (enrolamiento multi-agente) over a real
// HTTP server backed by a SQLite file:
//
//   - an ENROLLMENT token adopts N DIFFERENT agents (1:N by RLS_NAME) with
//     the token's tags inherited AT ADOPTION;
//   - a name owned by ANOTHER ACTIVE token cannot be stolen (401);
//   - every token created by the API is a single-VPS enrollment token, even
//     if a stale client sends enrollment=false; legacy 1:1 rows are seeded
//     through the store and remain compatible;
//   - PUT /tokens/{id}/tags updates the token but does NOT re-write the
//     fleet already adopted (tags are fixed at adoption — documented
//     contract of the enrollment model);
//   - revoking an enrollment token auto-dismisses EVERY adopted agent and
//     its revoked token can no longer beat (401);
//   - a NEW enrollment token can adopt rows released by a REVOKED owner
//     and resurrect them with the same heartbeat flow.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// task13Token mirrors the create/list token contract (TASK-013 fields).
type task13Token struct {
	ID           int64    `json:"id"`
	Token        *string  `json:"token"`
	Name         string   `json:"name"`
	Tags         []string `json:"tags"`
	AgentID      *int64   `json:"agent_id"`
	Status       string   `json:"status"`
	Enrollment   bool     `json:"enrollment"`
	VPSScoped    bool     `json:"vps_scoped"`
	VPSID        *string  `json:"vps_id"`
	AdoptedCount int64    `json:"adopted_count"`
}

// task13AgentEntry is the agent-list projection slice needed here.
type task13AgentEntry struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Tags        []string `json:"tags"`
	DismissedAt *int64   `json:"dismissed_at"`
}

func task13ListAgents(t *testing.T, admin *http.Client, srv *httptest.Server, scope string) []task13AgentEntry {
	t.Helper()
	url := srv.URL + "/api/v1/agents"
	if scope != "" {
		url += "?scope=" + scope
	}
	r := do(t, admin, http.MethodGet, url, "")
	if r.status != http.StatusOK {
		t.Fatalf("agents list: %d %s", r.status, r.body)
	}
	var out []task13AgentEntry
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatalf("agents list body: %v (%s)", err, r.body)
	}
	return out
}

func task13ListTokens(t *testing.T, admin *http.Client, srv *httptest.Server) []task13Token {
	t.Helper()
	r := do(t, admin, http.MethodGet, srv.URL+"/api/v1/tokens", "")
	if r.status != http.StatusOK {
		t.Fatalf("tokens list: %d %s", r.status, r.body)
	}
	var out []task13Token
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatalf("tokens list body: %v (%s)", err, r.body)
	}
	return out
}

func task13TokenByName(list []task13Token, name string) task13Token {
	for _, tk := range list {
		if tk.Name == name {
			return tk
		}
	}
	return task13Token{}
}

// task13Beat builds an enrollment heartbeat for one RLS_NAME/hostname.
func task13Beat(name, hostname string, tsMS int64, engineIDs ...string) string {
	engineID := "engine-task13"
	if len(engineIDs) > 0 && engineIDs[0] != "" {
		engineID = engineIDs[0]
	}
	return `{"hostname":` + strconv.Quote(hostname) + `,"name":` + strconv.Quote(name) +
		`,"ts":` + strconv.FormatInt(tsMS, 10) + `,"interval":5,"labels":{"rls_vps_id":` + strconv.Quote(engineID) + `},
		  "metrics":{"cpu_pct":9.9,"ram_used_mb":80.1,"ram_limit_mb":256}}`
}

func TestE2E_EnrollmentTokens(t *testing.T) {
	srv, _, _ := newTask4Server(t)
	admin := bootstrapAdmin(t, srv)

	// ---- 1. mint the fleet token ----------------------------------------
	r := do(t, admin, http.MethodPost, srv.URL+"/api/v1/tokens",
		`{"name":"fleet-prod","tags":["lab","fleet"],"enrollment":true}`)
	if r.status != http.StatusCreated {
		t.Fatalf("create enrollment token: got %d %s", r.status, r.body)
	}
	var created task13Token
	if err := json.Unmarshal([]byte(r.body), &created); err != nil {
		t.Fatalf("create body: %v (%s)", err, r.body)
	}
	if !created.Enrollment || created.Token == nil || !strings.HasPrefix(*created.Token, "rlst_") {
		t.Fatalf("enrollment token fields wrong: %+v", created)
	}
	fleet := *created.Token
	fs := task13TokenByName(task13ListTokens(t, admin, srv), "fleet-prod")
	if !fs.Enrollment || fs.AdoptedCount != 0 || fs.AgentID != nil || fs.Status != "active" {
		t.Fatalf("fresh enrollment token in list: %+v", fs)
	}

	// ---- 2. two DIFFERENT containers adopt with the same token ----------
	if b := beat(t, srv, fleet, task13Beat("smoke-a", "web-1", validBeatTS)); b.status != http.StatusOK {
		t.Fatalf("beat smoke-a: %d %s", b.status, b.body)
	}
	if b := beat(t, srv, fleet, task13Beat("smoke-b", "web-2", validBeatTS+5_000)); b.status != http.StatusOK {
		t.Fatalf("beat smoke-b: %d %s", b.status, b.body)
	}
	agents := task13ListAgents(t, admin, srv, "")
	if len(agents) != 2 {
		t.Fatalf("default view should show the 2 adopted agents: %+v (%s)", agents, r.body)
	}
	names := map[string]task13AgentEntry{}
	for _, a := range agents {
		names[a.Name] = a
	}
	for _, want := range []string{"smoke-a", "smoke-b"} {
		a, ok := names[want]
		if !ok || a.Status != "online" || a.DismissedAt != nil {
			t.Fatalf("adopted agent %s wrong: %+v", want, a)
		}
		if len(a.Tags) != 2 || a.Tags[0] != "lab" || a.Tags[1] != "fleet" {
			t.Fatalf("agent %s did not inherit token tags: %v", want, a.Tags)
		}
	}
	fs = task13TokenByName(task13ListTokens(t, admin, srv), "fleet-prod")
	if fs.AdoptedCount != 2 || fs.AgentID != nil {
		t.Fatalf("adopted_count after 2 adoptions: %+v", fs)
	}

	// ---- 3. name stealing from ANOTHER ACTIVE token → 401 ---------------
	r = do(t, admin, http.MethodPost, srv.URL+"/api/v1/tokens",
		`{"name":"fleet-rival","enrollment":true}`)
	if r.status != http.StatusCreated {
		t.Fatalf("create rival token: %d %s", r.status, r.body)
	}
	var rival task13Token
	if err := json.Unmarshal([]byte(r.body), &rival); err != nil {
		t.Fatalf("rival body: %v", err)
	}
	if b := beat(t, srv, *rival.Token, task13Beat("smoke-a", "evil-1", validBeatTS+10_000, "engine-rival")); b.status != http.StatusUnauthorized {
		t.Fatalf("name stealing: got %d %s", b.status, b.body)
	}
	// …and the victim stays exactly as it was (single owner, untouched).
	if a := task13ListAgents(t, admin, srv, ""); len(a) != 2 {
		t.Fatalf("401 beat must not create rows: %+v", a)
	}

	// ---- 4. identity-less enrollment beat → 400 -------------------------
	naked := `{"hostname":"","ts":` + strconv.FormatInt(validBeatTS+15_000, 10) +
		`,"interval":5,"metrics":{"cpu_pct":1}}`
	if b := beat(t, srv, fleet, naked); b.status != http.StatusBadRequest {
		t.Fatalf("nameless beat: got %d %s", b.status, b.body)
	}

	// ---- 5. new API credentials have only the VPS enrollment mode ---------
	r = do(t, admin, http.MethodPost, srv.URL+"/api/v1/tokens", `{"name":"solo","tags":["solo"],"enrollment":false}`)
	if r.status != http.StatusCreated {
		t.Fatalf("create solo token: %d %s", r.status, r.body)
	}
	var solo task13Token
	if err := json.Unmarshal([]byte(r.body), &solo); err != nil {
		t.Fatalf("solo body: %v", err)
	}
	if !solo.Enrollment || !solo.VPSScoped {
		t.Fatalf("new token must be VPS-scoped enrollment regardless of stale client mode: %+v", solo)
	}
	if b := beat(t, srv, *solo.Token, task13Beat("solo-one", "web-9", validBeatTS+20_000, "engine-solo")); b.status != http.StatusOK {
		t.Fatalf("solo beat: %d %s", b.status, b.body)
	}
	fs = task13TokenByName(task13ListTokens(t, admin, srv), "solo")
	if !fs.Enrollment || fs.AdoptedCount != 1 || fs.AgentID != nil {
		t.Fatalf("VPS token binding: %+v", fs)
	}
	_ = task13ListAgents(t, admin, srv, "") // both VPS credentials coexist

	// ---- 6. PUT tags on the enrollment token: token yes, fleet NO -------
	r = do(t, admin, http.MethodPut, fmt.Sprintf("%s/api/v1/tokens/%d/tags", srv.URL, created.ID),
		`{"tags":["relabeled"]}`)
	if r.status != http.StatusOK {
		t.Fatalf("put enrollment tags: %d %s", r.status, r.body)
	}
	for _, a := range task13ListAgents(t, admin, srv, "") {
		if a.Name == "smoke-a" || a.Name == "smoke-b" {
			if len(a.Tags) != 2 || a.Tags[0] != "lab" || a.Tags[1] != "fleet" {
				t.Fatalf("enrollment fleet re-synced by PUT tags (must NOT): %s %v", a.Name, a.Tags)
			}
		}
	}
	fs = task13TokenByName(task13ListTokens(t, admin, srv), "fleet-prod")
	if fs.AdoptedCount != 2 {
		t.Fatalf("enrollment adopted_count drifted: %+v", fs)
	}

	// Re-beating the SAME token must never touch tags again either.
	if b := beat(t, srv, fleet, task13Beat("smoke-a", "web-1", validBeatTS+25_000)); b.status != http.StatusOK {
		t.Fatalf("re-beat smoke-a: %d %s", b.status, b.body)
	}
	for _, a := range task13ListAgents(t, admin, srv, "") {
		if a.Name == "smoke-a" && (len(a.Tags) != 2 || a.Tags[0] != "lab") {
			t.Fatalf("re-beat rewrote the inherited tags: %+v", a)
		}
	}

	// ---- 7. revoke the fleet → EVERY adopted agent auto-dismissed -------
	r = do(t, admin, http.MethodDelete, fmt.Sprintf("%s/api/v1/tokens/%d", srv.URL, created.ID), "")
	if r.status != http.StatusNoContent {
		t.Fatalf("revoke fleet: %d %s", r.status, r.body)
	}
	defaultAgents := task13ListAgents(t, admin, srv, "")
	if len(defaultAgents) != 1 || defaultAgents[0].Name != "solo-one" {
		t.Fatalf("default view after revoke should only show solo-one: %+v", defaultAgents)
	}
	all := task13ListAgents(t, admin, srv, "all")
	dismissed := map[string]*int64{}
	for _, a := range all {
		dismissed[a.Name] = a.DismissedAt
	}
	if dismissed["smoke-a"] == nil || dismissed["smoke-b"] == nil {
		t.Fatalf("fleet members not dismissed at revoke: %+v", dismissed)
	}
	if dismissed["solo-one"] != nil {
		t.Fatalf("unrelated solo agent was dismissed: %+v", all)
	}
	// ...and the revoked fleet token cannot beat a member back to life.
	if b := beat(t, srv, fleet, task13Beat("smoke-a", "web-1", validBeatTS+30_000)); b.status != http.StatusUnauthorized {
		t.Fatalf("revoked fleet beat: got %d %s", b.status, b.body)
	}

	// ---- 8. a NEW enrollment token adopts the released rows --------------
	r = do(t, admin, http.MethodPost, srv.URL+"/api/v1/tokens", `{"name":"fleet-v2","enrollment":true}`)
	if r.status != http.StatusCreated {
		t.Fatalf("create fleet-v2: %d %s", r.status, r.body)
	}
	var v2 task13Token
	if err := json.Unmarshal([]byte(r.body), &v2); err != nil {
		t.Fatalf("fleet-v2 body: %v", err)
	}
	// Tagless token: adoption must NOT wipe the agent's existing tags.
	if b := beat(t, srv, *v2.Token, task13Beat("smoke-b", "web-2", validBeatTS+40_000)); b.status != http.StatusOK {
		t.Fatalf("fleet-v2 adopt beat: %d %s", b.status, b.body)
	}
	nowAgents := task13ListAgents(t, admin, srv, "")
	found := false
	for _, a := range nowAgents {
		if a.Name == "smoke-b" {
			found = true
			if a.Status != "online" || a.DismissedAt != nil {
				t.Fatalf("adopted agent not resurrected: %+v", a)
			}
		}
		if a.Name == "smoke-a" {
			t.Fatalf("fleet-v2 must only adopt the beaten row: %+v", a)
		}
	}
	if !found {
		t.Fatalf("smoke-b missing after adoption: %+v", nowAgents)
	}
	fs = task13TokenByName(task13ListTokens(t, admin, srv), "fleet-v2")
	if fs.AdoptedCount != 1 {
		t.Fatalf("fleet-v2 adopted_count wrong: %+v", fs)
	}
	// The owner row of the dismissed fleet kept its record (soft record).
	if b := task13TokenByName(task13ListTokens(t, admin, srv), "fleet-prod"); b.Status != "revoked" {
		t.Fatalf("fleet-prod should list revoked: %+v", b)
	}
}
