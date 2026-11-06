package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSendHeartbeatUsesEnrollmentBearerContract(t *testing.T) {
	var received heartbeat
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agents/heartbeat" || r.Method != http.MethodPost {
			t.Errorf("unexpected heartbeat request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer rlst_enrollment-secret" {
			t.Errorf("unexpected authorization header %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("unexpected content type %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode heartbeat: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer server.Close()

	collector, err := NewCollector(Config{
		DashboardURL: server.URL,
		Token:        "rlst_enrollment-secret",
		Hostname:     "dokploy-01",
		DockerSocket: "/unused-in-test",
		Interval:     10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	collector.remote = server.Client()
	cpu := 13.5
	err = collector.sendHeartbeat(context.Background(), heartbeat{
		Hostname: "dokploy-01", TS: 1793613600000, Interval: 10,
		Name: "dokploy-01-engine01_web_api_1", ContainerID: "container-id",
		Labels:  map[string]string{"dokploy_project": "web", "rls_collector": "stowaway"},
		Metrics: Metrics{CPUPct: &cpu},
	})
	if err != nil {
		t.Fatal(err)
	}
	if received.Name == "" || received.ContainerID != "container-id" || received.Interval != 10 || received.Metrics.CPUPct == nil || *received.Metrics.CPUPct != cpu {
		t.Fatalf("heartbeat contract mismatch: %+v", received)
	}
}

func TestVPSIdentityLabelsAreStableAndKeepContainerLabels(t *testing.T) {
	labels := map[string]string{
		"com.docker.compose.project": "billing",
		"rls_collector":              "stowaway",
	}

	addVPSIdentityLabels(labels, "engine-id-123456", "ubuntu")

	if labels["rls_vps_id"] != "engine-id-123456" || labels["rls_vps_name"] != "ubuntu" {
		t.Fatalf("VPS identity labels missing or incorrect: %#v", labels)
	}
	if labels["com.docker.compose.project"] != "billing" || labels["rls_collector"] != "stowaway" {
		t.Fatalf("adding VPS identity changed existing labels: %#v", labels)
	}
}
