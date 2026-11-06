package api

import (
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rls-legacy/captain/backend/internal/bridge"
)

var downloadHostPattern = regexp.MustCompile(`^(?:[A-Za-z0-9.-]+(?::[0-9]{1,5})?|\[[0-9A-Fa-f:]+\](?::[0-9]{1,5})?)$`)

func (s *Server) handleBridgeInstallScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	host := strings.TrimSpace(r.Host)
	defaultURL, err := dashboardBaseURL(host, r.Header.Get("X-Forwarded-Proto"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host")
		return
	}

	script, err := bridge.InstallScript()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "installer unavailable")
		return
	}
	// The host has been restricted to DNS/IP characters above, so it is safe
	// to insert as a single-quoted POSIX shell string literal.
	body := strings.ReplaceAll(string(script), "@@DASHBOARD_URL@@", defaultURL)
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func localRequestHost(host string) bool {
	name := host
	if strings.HasPrefix(name, "[") {
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			name = parsed
		} else {
			name = strings.Trim(name, "[]")
		}
	} else if parsed, _, err := net.SplitHostPort(host); err == nil {
		name = parsed
	}
	if strings.EqualFold(name, "localhost") || strings.HasSuffix(strings.ToLower(name), ".localhost") {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) handleBridgeDownload(w http.ResponseWriter, r *http.Request) {
	asset := chi.URLParam(r, "asset")
	var filename, contentType string
	switch asset {
	case "amd64":
		filename, contentType = "rls-stowaway-linux-amd64", "application/octet-stream"
	case "arm64":
		filename, contentType = "rls-stowaway-linux-arm64", "application/octet-stream"
	case "amd64.sha256":
		filename, contentType = "rls-stowaway-linux-amd64.sha256", "text/plain; charset=utf-8"
	case "arm64.sha256":
		filename, contentType = "rls-stowaway-linux-arm64.sha256", "text/plain; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	path := filepath.Join(s.downloadDir, filename)
	http.ServeFile(w, r, path)
}

// dashboardBaseURL validates and returns the base URL used by the installer's
// default value. Kept small and explicit to make proxy-host safety auditable.
func dashboardBaseURL(host, forwardedProto string) (string, error) {
	if !downloadHostPattern.MatchString(host) {
		return "", fmt.Errorf("invalid host")
	}
	scheme := "https"
	if strings.EqualFold(forwardedProto, "http") && localRequestHost(host) {
		scheme = "http"
	}
	if forwardedProto == "" && localRequestHost(host) {
		scheme = "http"
	}
	return scheme + "://" + host, nil
}
