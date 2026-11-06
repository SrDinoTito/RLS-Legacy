// alerts.go: alert listing and manual resolution (TASK-004). Alerts carry
// their agent name for the flat UI list; scope=active (default) limits the
// result to still-open rows.
package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/rls-legacy/captain/backend/internal/notify"
	"github.com/rls-legacy/captain/backend/internal/store"
)

// alertDTO is the JSON projection of an alert row (+agent display name).
// Pointer fields: NULL values (value/message/resolved_at/agent_name) render
// as literal null instead of {"Valid":false} objects.
type alertDTO struct {
	ID         int64    `json:"id"`
	AgentID    int64    `json:"agent_id"`
	AgentName  *string  `json:"agent_name"`
	Type       string   `json:"type"`
	Value      *float64 `json:"value"`
	Message    *string  `json:"message"`
	FiredAt    int64    `json:"fired_at"`
	ResolvedAt *int64   `json:"resolved_at"`
}

func alertToDTO(a store.AlertWithAgent) alertDTO {
	dto := alertDTO{ID: a.ID, AgentID: a.AgentID, Type: a.Type, FiredAt: a.FiredAt}
	if a.AgentName.Valid {
		dto.AgentName = &a.AgentName.String
	}
	if a.Message.Valid {
		dto.Message = &a.Message.String
	}
	if a.Value.Valid {
		dto.Value = &a.Value.Float64
	}
	if a.ResolvedAt.Valid {
		dto.ResolvedAt = &a.ResolvedAt.Int64
	}
	return dto
}

// GET /api/v1/alerts?scope=active|all — newest first. scope defaults to
// "active" (only rows whose resolved_at is NULL).
func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "active"
	}
	if scope != "active" && scope != "all" {
		writeError(w, http.StatusBadRequest, "scope must be 'active' or 'all'")
		return
	}
	alerts, err := s.store.ListAlertsAll(r.Context(), scope == "active")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	out := make([]alertDTO, 0, len(alerts))
	for _, a := range alerts {
		out = append(out, alertToDTO(a))
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/v1/alerts/{id}/resolve — stamps resolved_at = now. 404 before
// the fact if the alert does not exist; 409 when it was already resolved.
// Broadcasts alert_resolved so open dashboards can clear the badge.
func (s *Server) handleResolveAlert(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid alert id")
		return
	}
	alert, err := s.store.AlertByID(r.Context(), id)
	if err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, "alert not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if alert.ResolvedAt.Valid {
		writeError(w, http.StatusConflict, "alert already resolved")
		return
	}
	resolvedAt := s.now().Unix()
	if _, err := s.store.ResolveAlertByID(r.Context(), id, resolvedAt); err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	s.broker.Broadcast("alert_resolved", map[string]any{
		"id": id, "agent_id": alert.AgentID, "type": alert.Type,
		"resolved_at": resolvedAt,
	})
	// REQ-050: notify the resolution too — enriched via best-effort lookups
	// (agent name + tags + latest sample; a purged agent just degrades the
	// enrichment to the bare resolve message).
	s.notifyLookupEvent(r.Context(), notify.Event{
		AgentID: alert.AgentID, Type: alert.Type,
		Message: "alerta resuelta manualmente por un admin",
		TS:      resolvedAt, Kind: notify.KindResolved,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "resolved_at": resolvedAt,
	})
}
