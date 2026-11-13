package api

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// NormalizeDashboardURL validates the required public base URL shared by the
// install instructions, Stowaway downloads and notification deep links.
// Plain HTTP is accepted only for loopback development on the same machine.
func NormalizeDashboardURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("RLS_PUBLIC_URL is required (for example https://rls.example.com or http://localhost:3008)")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return "", fmt.Errorf("RLS_PUBLIC_URL must be an absolute http(s) URL")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("RLS_PUBLIC_URL scheme must be http or https")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("RLS_PUBLIC_URL must be only the dashboard origin, without credentials, path, query or fragment")
	}
	if !downloadHostPattern.MatchString(u.Host) {
		return "", fmt.Errorf("RLS_PUBLIC_URL contains an invalid host")
	}
	if u.Scheme == "http" {
		host := strings.ToLower(u.Hostname())
		ip := net.ParseIP(host)
		loopback := host == "localhost" || strings.HasSuffix(host, ".localhost") || (ip != nil && ip.IsLoopback())
		if !loopback {
			return "", fmt.Errorf("RLS_PUBLIC_URL must use HTTPS outside localhost/loopback")
		}
	}
	u.Path = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func (s *Server) handlePublicConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"dashboard_url": s.dashboardURL})
}
