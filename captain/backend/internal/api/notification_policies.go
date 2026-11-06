package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rls-legacy/captain/backend/internal/store"
)

type notificationPolicyRequest struct {
	NotificationsEnabled  *bool `json:"notifications_enabled"`
	ResourceAlertsEnabled *bool `json:"resource_alerts_enabled"`
}

// GET /api/v1/notification-policies/{scope}/{scopeID}.
// Agent settings inherit the current VPS policy unless explicitly customized.
func (s *Server) handleGetNotificationPolicy(w http.ResponseWriter, r *http.Request) {
	scope, scopeID, ok := notificationPolicyPath(w, r)
	if !ok {
		return
	}
	policy, err := s.store.GetNotificationPolicy(r.Context(), scope, scopeID)
	if err != nil {
		writeNotificationPolicyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

// PUT /api/v1/notification-policies/{scope}/{scopeID}.
// Both values are saved atomically as the explicit policy for the scope.
func (s *Server) handlePutNotificationPolicy(w http.ResponseWriter, r *http.Request) {
	scope, scopeID, ok := notificationPolicyPath(w, r)
	if !ok {
		return
	}
	var body notificationPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.NotificationsEnabled == nil || body.ResourceAlertsEnabled == nil {
		writeError(w, http.StatusBadRequest, "notifications_enabled and resource_alerts_enabled are required")
		return
	}
	if err := s.store.SetNotificationPolicy(r.Context(), scope, scopeID,
		*body.NotificationsEnabled, *body.ResourceAlertsEnabled, s.now().Unix()); err != nil {
		writeNotificationPolicyError(w, err)
		return
	}
	policy, err := s.store.GetNotificationPolicy(r.Context(), scope, scopeID)
	if err != nil {
		writeNotificationPolicyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

// DELETE /api/v1/notification-policies/{scope}/{scopeID}.
// VPS scope resets to safe defaults; agent scope resumes inheritance.
func (s *Server) handleDeleteNotificationPolicy(w http.ResponseWriter, r *http.Request) {
	scope, scopeID, ok := notificationPolicyPath(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteNotificationPolicy(r.Context(), scope, scopeID); err != nil {
		writeNotificationPolicyError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func notificationPolicyPath(w http.ResponseWriter, r *http.Request) (scope, scopeID string, ok bool) {
	scope = chi.URLParam(r, "scope")
	scopeID = strings.TrimSpace(chi.URLParam(r, "scopeID"))
	if scope != store.NotificationScopeVPS && scope != store.NotificationScopeAgent {
		writeError(w, http.StatusBadRequest, "scope must be 'vps' or 'agent'")
		return "", "", false
	}
	if scopeID == "" {
		writeError(w, http.StatusBadRequest, "scope id required")
		return "", "", false
	}
	return scope, scopeID, true
}

func writeNotificationPolicyError(w http.ResponseWriter, err error) {
	switch {
	case err == store.ErrNotFound:
		writeError(w, http.StatusNotFound, "agent not found")
	case strings.Contains(err.Error(), "invalid agent id"),
		strings.Contains(err.Error(), "scope must"),
		strings.Contains(err.Error(), "scope id required"):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "database error")
	}
}
