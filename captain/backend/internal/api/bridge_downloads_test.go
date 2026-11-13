package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rls-legacy/captain/backend/internal/api"
	"github.com/rls-legacy/captain/backend/internal/store"
)

const installerTestToken = "rlst_installer_test_0123456789"

func newInstallerToken(t *testing.T, vpsScoped bool) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "installer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	plain := installerTestToken
	if !vpsScoped {
		plain = "rlst_legacy_installer_test"
	}
	if _, err := st.CreateTokenFull(context.Background(), api.HashAgentToken(plain), plain, "installer-test", "", true, vpsScoped); err != nil {
		t.Fatal(err)
	}
	return st
}

func withInstallerToken(method, path, token string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request
}

func TestBridgeInstallerRequiresVpsTokenAndUsesConfiguredBase(t *testing.T) {
	st := newInstallerToken(t, true)
	handler := api.New(st, []byte("test-secret"), api.PublicConfig{DashboardURL: "https://observatory.example.test"}).Handler()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, withInstallerToken(http.MethodGet, "/script.sh", ""))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated installer request: got %d", response.Code)
	}

	request := withInstallerToken(http.MethodGet, "/script.sh", installerTestToken)
	request.Host = "attacker.example.test"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /script.sh: %d %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); !strings.Contains(got, "shellscript") {
		t.Fatalf("unexpected installer content type %q", got)
	}
	if got := response.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("installer cache policy = %q, want no-store", got)
	}
	body := response.Body.String()
	for _, expected := range []string{
		"DEFAULT_DASHBOARD_URL='https://observatory.example.test'",
		"DEFAULT_TOKEN='" + installerTestToken + "'",
		"Authorization: Bearer %s",
		"ARTIFACT_URL=$BASE_URL/downloads/rls-stowaway-linux/$ARCH",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("installer response missing %q", expected)
		}
	}
	if strings.Contains(body, "@@DASHBOARD_URL@@") || strings.Contains(body, "@@TOKEN@@") {
		t.Fatal("installer response contains an unresolved placeholder")
	}

	legacy := newInstallerToken(t, false)
	legacyPlain := "rlst_legacy_installer_test"
	legacyHandler := api.New(legacy, []byte("test-secret"), api.PublicConfig{DashboardURL: "https://observatory.example.test"}).Handler()
	response = httptest.NewRecorder()
	legacyHandler.ServeHTTP(response, withInstallerToken(http.MethodGet, "/script.sh", legacyPlain))
	if response.Code != http.StatusForbidden {
		t.Fatalf("legacy token installer request: got %d, want 403", response.Code)
	}
}

func TestBridgeDownloadsRequireTheSameActiveVpsToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RLS_BRIDGE_DOWNLOAD_DIR", dir)
	for _, arch := range []string{"amd64", "arm64"} {
		name := "rls-stowaway-linux-" + arch
		binary := []byte("mock binary for " + arch)
		if err := os.WriteFile(filepath.Join(dir, name), binary, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(binary)
		checksum := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)
		if err := os.WriteFile(filepath.Join(dir, name+".sha256"), []byte(checksum), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st := newInstallerToken(t, true)
	handler := api.New(st, []byte("test-secret"), api.PublicConfig{DashboardURL: "http://localhost:3008"}).Handler()

	for _, path := range []string{
		"/downloads/rls-stowaway-linux/amd64",
		"/downloads/rls-stowaway-linux/amd64.sha256",
		"/downloads/rls-stowaway-linux/arm64",
		"/downloads/rls-stowaway-linux/arm64.sha256",
	} {
		request := withInstallerToken(http.MethodGet, path, installerTestToken)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("expected allowlisted artifact %s, got %d: %s", path, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
			t.Fatalf("artifact %s cache policy = %q, want no-store", path, got)
		}
	}

	request := withInstallerToken(http.MethodGet, "/downloads/rls-stowaway-linux/amd64", "")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated artifact request: got %d", response.Code)
	}

	for _, path := range []string{
		"/downloads/rls-stowaway-linux/../../etc/passwd",
		"/downloads/rls-stowaway-linux/mips",
	} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, withInstallerToken(http.MethodGet, path, installerTestToken))
		if response.Code != http.StatusNotFound {
			t.Fatalf("unexpected response for non-allowlisted asset %s: %d %s", path, response.Code, response.Body.String())
		}
	}

	if err := st.RevokeToken(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, withInstallerToken(http.MethodGet, "/downloads/rls-stowaway-linux/amd64", installerTestToken))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token download request: got %d, want 401", response.Code)
	}
}

func TestPublicConfigReturnsConfiguredDashboardURL(t *testing.T) {
	handler := api.New(nil, []byte("test-secret"), api.PublicConfig{DashboardURL: "http://localhost:3008"}).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/public-config", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"dashboard_url":"http://localhost:3008"`) {
		t.Fatalf("public config response: %d %s", response.Code, response.Body.String())
	}
}
