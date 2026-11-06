package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

type containerTagAgent struct {
	ID            int64    `json:"id"`
	Tags          []string `json:"tags"`
	ContainerTags []string `json:"container_tags"`
}

func TestE2E_ContainerTagsAreIndependentAndAdminManaged(t *testing.T) {
	srv, _, _ := newTask4Server(t)
	admin := bootstrapAdmin(t, srv)
	base := srv.URL + "/api/v1"

	created := do(t, admin, http.MethodPost, base+"/tokens", `{"name":"vps-test","tags":["fleet"]}`)
	if created.status != http.StatusCreated {
		t.Fatalf("create token: %d %s", created.status, created.body)
	}
	var token task11Token
	if err := json.Unmarshal([]byte(created.body), &token); err != nil || token.Token == nil {
		t.Fatalf("decode token: %v (%s)", err, created.body)
	}
	if beat(t, srv, *token.Token, withVPSID(validBeat(), "engine-container-tags")).status != http.StatusOK {
		t.Fatal("agent heartbeat was rejected")
	}

	listAgents := func() []containerTagAgent {
		t.Helper()
		r := do(t, admin, http.MethodGet, base+"/agents", "")
		var agents []containerTagAgent
		if r.status != http.StatusOK || json.Unmarshal([]byte(r.body), &agents) != nil || len(agents) != 1 {
			t.Fatalf("list agents: %d %s", r.status, r.body)
		}
		return agents
	}
	agentID := listAgents()[0].ID
	putTags := func(client *http.Client, id int64, body string) response {
		t.Helper()
		return do(t, client, http.MethodPut, fmt.Sprintf("%s/agents/%d/container-tags", base, id), body)
	}

	for _, invalid := range []string{`{}`, `{"tags":null}`, `{"tags":["Bad"]}`, `{"tags":["bad tag"]}`, `{"tags":["a","b","c","d","e","f"]}`} {
		if r := putTags(admin, agentID, invalid); r.status != http.StatusBadRequest {
			t.Fatalf("invalid payload %s: got %d %s", invalid, r.status, r.body)
		}
	}
	if r := putTags(admin, 999999, `{"tags":["web","cache"]}`); r.status != http.StatusNotFound {
		t.Fatalf("unknown agent: got %d %s", r.status, r.body)
	}
	if r := putTags(admin, agentID, `{"tags":[" web ","cache","web"]}`); r.status != http.StatusOK {
		t.Fatalf("save tags: %d %s", r.status, r.body)
	}
	agent := listAgents()[0]
	if len(agent.Tags) != 1 || agent.Tags[0] != "fleet" {
		t.Fatalf("container update changed inherited tags: %v", agent.Tags)
	}
	if len(agent.ContainerTags) != 2 || agent.ContainerTags[0] != "web" || agent.ContainerTags[1] != "cache" {
		t.Fatalf("unexpected container tags: %v", agent.ContainerTags)
	}
	if beat(t, srv, *token.Token, withVPSID(validBeat(), "engine-container-tags")).status != http.StatusOK {
		t.Fatal("subsequent heartbeat was rejected")
	}
	if got := listAgents()[0].ContainerTags; len(got) != 2 || got[0] != "web" || got[1] != "cache" {
		t.Fatalf("heartbeat overwrote per-container tags: %v", got)
	}

	if r := do(t, admin, http.MethodPost, base+"/users", `{"username":"viewer","password":"viewer-password-1","role":"viewer"}`); r.status != http.StatusCreated {
		t.Fatalf("create viewer: %d %s", r.status, r.body)
	}
	viewer := clientLoggedIn(t, srv, "viewer", "viewer-password-1")
	if r := putTags(viewer, agentID, `{"tags":["private"]}`); r.status != http.StatusForbidden {
		t.Fatalf("viewer edited tags: %d %s", r.status, r.body)
	}
	if r := putTags(admin, agentID, `{"tags":[]}`); r.status != http.StatusOK {
		t.Fatalf("clear tags: %d %s", r.status, r.body)
	}
	if got := listAgents()[0].ContainerTags; len(got) != 0 {
		t.Fatalf("clear did not remove tags: %v", got)
	}
}
