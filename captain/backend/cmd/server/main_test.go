package main

import "testing"

func TestConfigRequiresDashboardURL(t *testing.T) {
	t.Setenv("RLS_PUBLIC_URL", "")
	if _, err := configFromEnv(); err == nil {
		t.Fatal("configFromEnv accepted an empty RLS_PUBLIC_URL")
	}
}

func TestConfigAcceptsLocalAndSecureDashboardURLs(t *testing.T) {
	t.Setenv("RLS_PUBLIC_URL", "http://localhost:3008/")
	t.Setenv("RLS_JWT_SECRET", "test-secret-with-at-least-32-characters")
	cfg, err := configFromEnv()
	if err != nil {
		t.Fatalf("local dashboard URL: %v", err)
	}
	if cfg.DashboardURL != "http://localhost:3008" || cfg.Notifier.PublicURL != cfg.DashboardURL {
		t.Fatalf("dashboard URL was not normalized or shared: dashboard=%q notify=%q", cfg.DashboardURL, cfg.Notifier.PublicURL)
	}

	t.Setenv("RLS_PUBLIC_URL", "https://rls.example.test")
	cfg, err = configFromEnv()
	if err != nil || cfg.DashboardURL != "https://rls.example.test" {
		t.Fatalf("HTTPS dashboard URL: got %q, err=%v", cfg.DashboardURL, err)
	}
}

func TestConfigRejectsRemoteHTTPDashboardURL(t *testing.T) {
	t.Setenv("RLS_PUBLIC_URL", "http://rls.example.test")
	if _, err := configFromEnv(); err == nil {
		t.Fatal("configFromEnv accepted remote HTTP")
	}
}
