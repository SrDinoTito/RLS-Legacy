package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

// writeJSON serializes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes the standard error envelope {"error":"..."}.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// credsRequest is the shared payload for bootstrap, login and user creation.
type credsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// validRoles is the whitelist of dashboard roles.
var validRoles = map[string]bool{"admin": true, "viewer": true}

// validateCreds enforces the API contract:
// username >= 3 chars, password >= 8 chars, role in {admin, viewer}.
// It returns a descriptive error message or "" when valid.
func validateCreds(username, password, role string) string {
	if len(username) < 3 {
		return "username must be at least 3 characters"
	}
	if len(password) < 8 {
		return "password must be at least 8 characters"
	}
	if !validRoles[role] {
		return "role must be 'admin' or 'viewer'"
	}
	return ""
}

// decodeCreds parses and normalizes a JSON credentials body.
func decodeCreds(r *http.Request) (credsRequest, error) {
	var c credsRequest
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		return c, err
	}
	c.Username = strings.TrimSpace(c.Username)
	c.Role = strings.TrimSpace(c.Role)
	return c, nil
}
