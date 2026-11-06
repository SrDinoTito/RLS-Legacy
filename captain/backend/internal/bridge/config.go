// Package bridge implements the host-side Docker collector used by RLS Stowaway.
package bridge

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config contains the bridge's runtime settings. The token is never logged.
type Config struct {
	DashboardURL string
	Token        string
	Hostname     string
	DockerSocket string
	Interval     time.Duration
}

// ConfigFromEnv resolves settings from the systemd EnvironmentFile.
func ConfigFromEnv() (Config, error) {
	dashboard := strings.TrimRight(strings.TrimSpace(os.Getenv("RLS_DASHBOARD_URL")), "/")
	token := strings.TrimSpace(os.Getenv("RLS_TOKEN"))
	if dashboard == "" || token == "" {
		return Config{}, fmt.Errorf("RLS_DASHBOARD_URL and RLS_TOKEN are required")
	}
	if err := validateDashboardURL(dashboard); err != nil {
		return Config{}, err
	}

	intervalSec := 10
	if raw := strings.TrimSpace(os.Getenv("RLS_INTERVAL")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 5 || parsed > 300 {
			return Config{}, fmt.Errorf("RLS_INTERVAL must be an integer between 5 and 300 seconds")
		}
		intervalSec = parsed
	}

	hostname := strings.TrimSpace(os.Getenv("RLS_HOSTNAME"))
	if hostname == "" {
		var err error
		hostname, err = os.Hostname()
		if err != nil || strings.TrimSpace(hostname) == "" {
			return Config{}, fmt.Errorf("unable to determine host name")
		}
	}
	socket := strings.TrimSpace(os.Getenv("RLS_DOCKER_SOCKET"))
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	return Config{
		DashboardURL: dashboard,
		Token:        token,
		Hostname:     hostname,
		DockerSocket: socket,
		Interval:     time.Duration(intervalSec) * time.Second,
	}, nil
}

func validateDashboardURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("RLS_DASHBOARD_URL must be an absolute http(s) base URL")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("RLS_DASHBOARD_URL must use HTTPS (HTTP is allowed only on localhost)")
	}
	if u.Scheme == "http" && !isLocalHost(u.Hostname()) {
		return fmt.Errorf("RLS_DASHBOARD_URL must use HTTPS except for localhost")
	}
	return nil
}

func isLocalHost(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
