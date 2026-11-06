package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rls-legacy/captain/backend/internal/store"
)

// PUT /api/v1/agents/{id}/container-tags — replace the operator-managed
// container tags. Token/system tags are intentionally stored separately.
func (s *Server) handlePutAgentContainerTags(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid agent id")
		return
	}
	var body struct {
		Tags *[]string `json:"tags"`
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err != nil || len(data) == 0 {
		writeError(w, http.StatusBadRequest, "request body required")
		return
	}
	if err := json.Unmarshal(data, &body); err != nil || body.Tags == nil {
		writeError(w, http.StatusBadRequest, "tags array required")
		return
	}
	tags, err := normalizeTags(*body.Tags)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agent, err := s.store.AgentByID(r.Context(), id)
	if err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if err := s.store.SetAgentContainerTags(r.Context(), id, tagsToJSON(tags)); err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	// Existing clients refetch GET /agents on heartbeat, so this also updates
	// connected dashboards without adding another SSE event contract.
	s.broadcastHeartbeat(agent.ID, agent.Name, agent.Status)
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "container_tags": tags})
}
