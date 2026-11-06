package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rls-legacy/captain/backend/internal/api"
)

func vpsTestBeat(name, vpsID string, ts int64, cpu float64) string {
	body := struct {
		Hostname    string             `json:"hostname"`
		Name        string             `json:"name"`
		ContainerID string             `json:"container_id"`
		TS          int64              `json:"ts"`
		Interval    int                `json:"interval"`
		Labels      map[string]string  `json:"labels"`
		Metrics     map[string]float64 `json:"metrics"`
	}{
		Hostname: "docker-host", Name: name, ContainerID: "container-" + name,
		TS: ts, Interval: 5,
		Labels:  map[string]string{"rls_vps_id": vpsID, "rls_vps_name": "test-vps"},
		Metrics: map[string]float64{"cpu_pct": cpu, "ram_used_mb": 1, "ram_limit_mb": 10},
	}
	encoded, _ := json.Marshal(body)
	return string(encoded)
}

func createVPSTestToken(t *testing.T, admin *http.Client, srvURL, name string) (int64, string) {
	t.Helper()
	r := do(t, admin, http.MethodPost, srvURL+"/api/v1/tokens", `{"name":"`+name+`"}`)
	if r.status != http.StatusCreated {
		t.Fatalf("create VPS token %s: %d %s", name, r.status, r.body)
	}
	var body struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(r.body), &body); err != nil || body.ID == 0 || body.Token == "" {
		t.Fatalf("decode VPS token: %v (%s)", err, r.body)
	}
	return body.ID, body.Token
}

func TestE2E_VPSTokenPinnedToOneDockerEngine(t *testing.T) {
	srv, _, _ := newTask4Server(t)
	admin := bootstrapAdmin(t, srv)
	tokenID, token := createVPSTestToken(t, admin, srv.URL, "vps-a")

	if b := beat(t, srv, token, `{"hostname":"docker-host","name":"missing-engine","metrics":{"cpu_pct":1}}`); b.status != http.StatusBadRequest {
		t.Fatalf("new VPS token must require a stable Docker Engine identity: %d %s", b.status, b.body)
	}
	if b := beat(t, srv, token, vpsTestBeat("web", "engine-a", validBeatTS, 10)); b.status != http.StatusOK {
		t.Fatalf("first engine beat: %d %s", b.status, b.body)
	}
	if b := beat(t, srv, token, vpsTestBeat("worker", "engine-a", validBeatTS+5_000, 10)); b.status != http.StatusOK {
		t.Fatalf("second container from same VPS: %d %s", b.status, b.body)
	}
	if b := beat(t, srv, token, vpsTestBeat("other-vps-container", "engine-b", validBeatTS+10_000, 10)); b.status != http.StatusConflict {
		t.Fatalf("same token must reject another Docker Engine: %d %s", b.status, b.body)
	}
	if got := len(task13ListAgents(t, admin, srv, "")); got != 2 {
		t.Fatalf("rejected VPS must not create an agent; got %d rows", got)
	}
	first := task13TokenByName(task13ListTokens(t, admin, srv), "vps-a")
	if !first.VPSScoped || first.VPSID == nil || *first.VPSID != "engine-a" || first.AdoptedCount != 2 {
		t.Fatalf("token binding/list projection wrong: %+v", first)
	}

	secondID, second := createVPSTestToken(t, admin, srv.URL, "vps-b")
	if b := beat(t, srv, second, vpsTestBeat("vps-b-app", "engine-a", validBeatTS+15_000, 10)); b.status != http.StatusConflict {
		t.Fatalf("one active VPS must not get two active tokens: %d %s", b.status, b.body)
	}
	if r := do(t, admin, http.MethodDelete, fmt.Sprintf("%s/api/v1/tokens/%d", srv.URL, tokenID), ""); r.status != http.StatusNoContent {
		t.Fatalf("revoke first VPS token: %d %s", r.status, r.body)
	}
	if b := beat(t, srv, second, vpsTestBeat("vps-b-app", "engine-a", validBeatTS+20_000, 10)); b.status != http.StatusOK {
		t.Fatalf("revoking prior token must release VPS binding: %d %s", b.status, b.body)
	}
	secondRow := task13TokenByName(task13ListTokens(t, admin, srv), "vps-b")
	if secondRow.VPSID == nil || *secondRow.VPSID != "engine-a" || secondRow.ID != secondID {
		t.Fatalf("replacement token did not claim released VPS: %+v", secondRow)
	}
}

func TestE2E_IgnoreOneContainerWithoutDisablingItsVPS(t *testing.T) {
	srv, st, _ := newTask4Server(t)
	admin := bootstrapAdmin(t, srv)
	_, token := createVPSTestToken(t, admin, srv.URL, "ignore-vps")
	if b := beat(t, srv, token, vpsTestBeat("noisy-app", "engine-ignore", validBeatTS, 99)); b.status != http.StatusOK {
		t.Fatalf("initial metric beat: %d %s", b.status, b.body)
	}
	if b := beat(t, srv, token, vpsTestBeat("quiet-app", "engine-ignore", validBeatTS+5_000, 5)); b.status != http.StatusOK {
		t.Fatalf("sibling container beat: %d %s", b.status, b.body)
	}
	agents := task13ListAgents(t, admin, srv, "")
	var noisyID int64
	for _, agent := range agents {
		if agent.Name == "noisy-app" {
			noisyID = agent.ID
		}
	}
	if noisyID == 0 || len(agents) != 2 {
		t.Fatalf("expected two adopted containers: %+v", agents)
	}
	if metrics, err := st.CountMetricsForAgent(t.Context(), noisyID); err != nil || metrics != 1 {
		t.Fatalf("initial metric count=%d err=%v", metrics, err)
	}
	alerts, err := st.ListAlerts(t.Context(), noisyID)
	if err != nil || len(alerts) != 1 || alerts[0].Type != "high_cpu" || alerts[0].ResolvedAt.Valid {
		t.Fatalf("high CPU alert should be open before exclusion: %+v err=%v", alerts, err)
	}

	result := do(t, admin, http.MethodPut, fmt.Sprintf("%s/api/v1/agents/%d/monitoring", srv.URL, noisyID), `{"ignored":true}`)
	if result.status != http.StatusOK || !strings.Contains(result.body, `"alerts_resolved":1`) {
		t.Fatalf("exclude container: %d %s", result.status, result.body)
	}
	if b := beat(t, srv, token, vpsTestBeat("noisy-app", "engine-ignore", validBeatTS+10_000, 99)); b.status != http.StatusOK || !strings.Contains(b.body, `"status":"ignored"`) {
		t.Fatalf("ignored heartbeat: %d %s", b.status, b.body)
	}
	if metrics, err := st.CountMetricsForAgent(t.Context(), noisyID); err != nil || metrics != 1 {
		t.Fatalf("ignored beat must not add metrics: count=%d err=%v", metrics, err)
	}
	alerts, err = st.ListAlerts(t.Context(), noisyID)
	if err != nil || len(alerts) != 1 || !alerts[0].ResolvedAt.Valid {
		t.Fatalf("ignoring must resolve the open resource alert: %+v err=%v", alerts, err)
	}
	if online, err := st.ListOnlineAgents(t.Context()); err != nil || len(online) != 1 || online[0].Name != "quiet-app" {
		t.Fatalf("ignored container must be excluded from liveness checks, sibling retained: %+v err=%v", online, err)
	}

	result = do(t, admin, http.MethodPut, fmt.Sprintf("%s/api/v1/agents/%d/monitoring", srv.URL, noisyID), `{"ignored":false}`)
	if result.status != http.StatusOK || !strings.Contains(result.body, `"ignored":false`) {
		t.Fatalf("re-include container: %d %s", result.status, result.body)
	}
	if b := beat(t, srv, token, vpsTestBeat("noisy-app", "engine-ignore", validBeatTS+15_000, 99)); b.status != http.StatusOK {
		t.Fatalf("heartbeat after re-include: %d %s", b.status, b.body)
	}
	if metrics, err := st.CountMetricsForAgent(t.Context(), noisyID); err != nil || metrics != 2 {
		t.Fatalf("monitoring should resume after re-include: count=%d err=%v", metrics, err)
	}
	var ignoredFlag bool
	if err := json.Unmarshal([]byte(strconv.FormatBool(false)), &ignoredFlag); err != nil || ignoredFlag {
		t.Fatalf("sanity: %v %v", ignoredFlag, err)
	}
}

func TestE2E_LegacySharedEnrollmentTokenRemainsCompatible(t *testing.T) {
	srv, st, _ := newTask4Server(t)
	admin := bootstrapAdmin(t, srv)
	const plain = "rlst_legacy_shared"
	if _, err := st.CreateTokenFull(t.Context(), api.HashAgentToken(plain), plain, "legacy-shared", "", true, false); err != nil {
		t.Fatalf("seed prior shared enrollment credential: %v", err)
	}
	for i, id := range []string{"old-engine-a", "old-engine-b"} {
		if b := beat(t, srv, plain, vpsTestBeat("legacy-app-"+strconv.Itoa(i), id, validBeatTS+int64(i)*5_000, 5)); b.status != http.StatusOK {
			t.Fatalf("grandfathered shared token should continue reporting engine %s: %d %s", id, b.status, b.body)
		}
	}
	if got := len(task13ListAgents(t, admin, srv, "")); got != 2 {
		t.Fatalf("legacy shared credential lost compatibility; got %d agents", got)
	}
	row, err := st.TokenActiveByHash(t.Context(), api.HashAgentToken(plain))
	if err != nil || row.VPSScoped || row.VPSID.Valid {
		t.Fatalf("grandfathered shared token should not be silently rebound: %+v err=%v", row, err)
	}
}
