package api_test

import (
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
)

func TestBridgeInstallerUsesValidatedRequestHost(t *testing.T) {
	handler := api.New(nil, []byte("test-secret")).Handler()

	request := httptest.NewRequest(http.MethodGet, "/script.sh", nil)
	request.Host = "observatory.example.test"
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /script.sh: %d %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); !strings.Contains(got, "shellscript") {
		t.Fatalf("unexpected installer content type %q", got)
	}
	body := response.Body.String()
	if !strings.Contains(body, "DEFAULT_DASHBOARD_URL='https://observatory.example.test'") || strings.Contains(body, "@@DASHBOARD_URL@@") {
		t.Fatalf("installer did not receive dashboard URL default")
	}
	if strings.Contains(body, "RLS_TOKEN=\"rlst_test") {
		t.Fatalf("installer response must not contain credentials")
	}
	if !strings.Contains(body, "ARTIFACT_URL=$BASE_URL/downloads/rls-stowaway-linux/$ARCH") {
		t.Fatalf("installer artifact URL must match the allowlisted download route")
	}

	request = httptest.NewRequest(http.MethodGet, "/script.sh", nil)
	request.Host = "observatory.example.test' ; touch /tmp/pwned #"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unsafe Host was accepted: %d", response.Code)
	}
}

func TestBridgeDownloadsAreAllowlisted(t *testing.T) {
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
	handler := api.New(nil, []byte("test-secret")).Handler()
	for _, path := range []string{
		"/downloads/rls-stowaway-linux/amd64",
		"/downloads/rls-stowaway-linux/amd64.sha256",
		"/downloads/rls-stowaway-linux/arm64",
		"/downloads/rls-stowaway-linux/arm64.sha256",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("expected allowlisted artifact %s, got %d: %s", path, response.Code, response.Body.String())
		}
	}
	for _, path := range []string{
		"/downloads/rls-stowaway-linux/../../etc/passwd",
		"/downloads/rls-stowaway-linux/mips",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("unexpected response for non-allowlisted asset %s: %d %s", path, response.Code, response.Body.String())
		}
	}
}
