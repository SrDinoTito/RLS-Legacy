package api

import (
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rls-legacy/captain/backend/internal/bridge"
	"github.com/rls-legacy/captain/backend/internal/store"
)

var downloadHostPattern = regexp.MustCompile(`^(?:[A-Za-z0-9.-]+(?::[0-9]{1,5})?|\[[0-9A-Fa-f:]+\](?::[0-9]{1,5})?)$`)
var installTokenPattern = regexp.MustCompile(`^rlst_[A-Za-z0-9_-]+$`)

func (s *Server) handleBridgeInstallScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	dashboardURL, err := NormalizeDashboardURL(s.dashboardURL)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "RLS_PUBLIC_URL is not configured")
		return
	}
	token, _ := r.Context().Value(ctxKeyToken{}).(*store.Token)
	if token == nil || !token.TokenPlain.Valid || !installTokenPattern.MatchString(token.TokenPlain.String) {
		writeError(w, http.StatusForbidden, "token cannot be used for VPS installation")
		return
	}

	script, err := bridge.InstallScript()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "installer unavailable")
		return
	}
	// Both values are strictly validated before insertion into single-quoted
	// shell literals: the URL allows only an origin and the token is allowlisted.
	body := strings.ReplaceAll(string(script), "@@DASHBOARD_URL@@", dashboardURL)
	body = strings.ReplaceAll(body, "@@TOKEN@@", token.TokenPlain.String)
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
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
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	path := filepath.Join(s.downloadDir, filename)
	http.ServeFile(w, r, path)
}
