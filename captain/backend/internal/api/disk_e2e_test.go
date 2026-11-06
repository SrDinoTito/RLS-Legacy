package api_test

// End-to-end test of the explorer v4 disk snapshot contract: heartbeat with
// disk_free_mb/disk_total_mb (inside metrics, optional per the DFS-fail
// policy) → the snapshot persists on the agent row and GET /agents carries
// it; a heartbeat WITHOUT the fields keeps the previous values (COALESCE);
// an agent whose beats never carried them reports null.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// agentJSON mirrors the fields of GET /api/v1/agents the test touches.
type agentJSON struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	DiskFreeMB  *float64 `json:"disk_free_mb"`
	DiskTotalMB *float64 `json:"disk_total_mb"`
	Metrics     *struct {
		CPUPct *float64 `json:"cpu_pct"`
		Uptime *float64 `json:"uptime_sec"`
	} `json:"metrics"`
}

func listAgentsJSON(t *testing.T, cl *http.Client, srv string) []agentJSON {
	t.Helper()
	r := do(t, cl, http.MethodGet, srv+"/api/v1/agents", "")
	if r.status != 200 {
		t.Fatalf("GET /agents: got %d %s", r.status, r.body)
	}
	var out []agentJSON
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatalf("unmarshal agents: %v (%s)", err, r.body)
	}
	return out
}

func agentByName(t *testing.T, cl *http.Client, srv, name string) agentJSON {
	t.Helper()
	for _, a := range listAgentsJSON(t, cl, srv) {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("agent %q not found", name)
	return agentJSON{}
}

func sendBeat(t *testing.T, srv, token, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv+"/api/v1/agents/heartbeat",
		strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := clientFor().Do(req)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("heartbeat: got %d %s", resp.StatusCode, string(data))
	}
}

func TestE2E_AgentDiskSnapshot(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL

	// bootstrap + token de 1:1 para el agent con disco reportado
	r := do(t, nil, http.MethodPost, base+"/api/v1/auth/bootstrap",
		`{"username":"admin","password":"admin-password-1"}`)
	if r.status != 201 {
		t.Fatalf("bootstrap: got %d %s", r.status, r.body)
	}
	admin := clientLoggedIn(t, srv, "admin", "admin-password-1")
	r = do(t, admin, http.MethodPost, base+"/api/v1/tokens",
		`{"name":"disk-tok","enrollment":false}`)
	if r.status != 201 {
		t.Fatalf("create token: got %d %s", r.status, r.body)
	}
	var tok struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(r.body), &tok); err != nil || tok.Token == "" {
		t.Fatalf("token body: %s", r.body)
	}

	// Beat CON snapshot de disco: persiste y se expone en el listado.
	sendBeat(t, base, tok.Token, `{"hostname":"h1","name":"disk-agent","ts":1700000000000,"labels":{"rls_vps_id":"engine-disk"},
		"interval":10,"metrics":{"cpu_pct":9.5,"disk_free_mb":18841.6,"disk_total_mb":76293.4}}`)
	a := agentByName(t, admin, base, "disk-agent")
	if a.DiskFreeMB == nil || *a.DiskFreeMB != 18841.6 ||
		a.DiskTotalMB == nil || *a.DiskTotalMB != 76293.4 {
		t.Fatalf("disk snapshot lost: free=%v total=%v", a.DiskFreeMB, a.DiskTotalMB)
	}

	// Beat SIN campos de disco: el snapshot previo se conserva (COALESCE).
	sendBeat(t, base, tok.Token, `{"hostname":"h1","name":"disk-agent","ts":1700000010000,"labels":{"rls_vps_id":"engine-disk"},
		"interval":10,"metrics":{"cpu_pct":7.5}}`)
	a = agentByName(t, admin, base, "disk-agent")
	if a.DiskFreeMB == nil || *a.DiskFreeMB != 18841.6 ||
		a.DiskTotalMB == nil || *a.DiskTotalMB != 76293.4 {
		t.Fatalf("disk snapshot should persist: free=%v total=%v", a.DiskFreeMB, a.DiskTotalMB)
	}

	// Beat con SOLO free: total se conserva, free se actualiza (por canal).
	sendBeat(t, base, tok.Token, `{"hostname":"h1","name":"disk-agent","ts":1700000020000,"labels":{"rls_vps_id":"engine-disk"},
		"interval":10,"metrics":{"disk_free_mb":17771.2}}`)
	a = agentByName(t, admin, base, "disk-agent")
	if a.DiskFreeMB == nil || *a.DiskFreeMB != 17771.2 ||
		a.DiskTotalMB == nil || *a.DiskTotalMB != 76293.4 {
		t.Fatalf("per-channel coalesce: free=%v total=%v", a.DiskFreeMB, a.DiskTotalMB)
	}

	// Agent cuyo primer beat NUNCA trae disco → null (nunca reportado).
	r = do(t, admin, http.MethodPost, base+"/api/v1/tokens",
		`{"name":"disk-tok2","enrollment":false}`)
	if r.status != 201 {
		t.Fatalf("create token2: got %d %s", r.status, r.body)
	}
	var tok2 struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(r.body), &tok2); err != nil || tok2.Token == "" {
		t.Fatalf("token2 body: %s", r.body)
	}
	sendBeat(t, base, tok2.Token, `{"hostname":"h2","name":"plain-agent","ts":1700000030000,"labels":{"rls_vps_id":"engine-disk-2"},
		"interval":10,"metrics":{"cpu_pct":3.3}}`)
	a = agentByName(t, admin, base, "plain-agent")
	if a.DiskFreeMB != nil || a.DiskTotalMB != nil {
		t.Fatalf("expected null disk for agent that never reported it: free=%v total=%v",
			a.DiskFreeMB, a.DiskTotalMB)
	}
}
