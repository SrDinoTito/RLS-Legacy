package bridge

import (
	"strings"
	"testing"
	"time"
)

func TestConfigFromEnvDefaultsAndValidation(t *testing.T) {
	t.Setenv("RLS_DASHBOARD_URL", "https://captain.example.test/rls/")
	t.Setenv("RLS_TOKEN", "rlst_test-token")
	t.Setenv("RLS_HOSTNAME", "dokploy-01")
	t.Setenv("RLS_DOCKER_SOCKET", "")
	t.Setenv("RLS_INTERVAL", "")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DashboardURL != "https://captain.example.test/rls" || cfg.Interval != 10*time.Second || cfg.DockerSocket != "/var/run/docker.sock" {
		t.Fatalf("unexpected config: %+v", cfg)
	}

	t.Setenv("RLS_INTERVAL", "4")
	if _, err := ConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "between 5 and 300") {
		t.Fatalf("out-of-range interval was accepted: %v", err)
	}
	t.Setenv("RLS_INTERVAL", "15")
	t.Setenv("RLS_DASHBOARD_URL", "http://captain.example.test")
	if _, err := ConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("public HTTP dashboard URL was accepted: %v", err)
	}
	t.Setenv("RLS_DASHBOARD_URL", "http://localhost:3008")
	if _, err := ConfigFromEnv(); err != nil {
		t.Fatalf("local HTTP dashboard should be allowed: %v", err)
	}
}
