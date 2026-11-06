// agents_api.go: read-side endpoints for agents (TASK-004) — the list with
// latest metrics and open alert counts, the history series with
// server-side rollup, and the per-agent threshold pairs.
package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/rls-legacy/captain/backend/internal/store"
)

// sql.Null* types have no JSON marshaling in the standard library, so the
// DTOs below use pointer fields: nil ⇔ the value was NULL/absent, which
// json.Unmarshal renders as null — and nothing leaks {"Valid":false}.
//
// metricPoint is the latest available sample of one agent.
type metricPoint struct {
	TS         *int64   `json:"ts"`
	CPUPct     *float64 `json:"cpu_pct"`
	RamUsedMB  *float64 `json:"ram_used_mb"`
	RamLimitMB *float64 `json:"ram_limit_mb"`
	RxMB       *float64 `json:"rx_mb"`
	TxMB       *float64 `json:"tx_mb"`
	UptimeSec  *float64 `json:"uptime_sec"`
}

// agentDTO is one entry of GET /api/v1/agents. Labels is stored as raw JSON
// on the agent row and is emitted UNSTRINGIFIED (json.RawMessage), so
// `{"app":"nginx"}` on the wire, not `"\"{...}\""`. DismissedAt is null for
// active agents; a soft-dismissed agent (TASK-009, only visible with
// scope=all) carries its dismiss timestamp.
type agentDTO struct {
	ID            int64           `json:"id"`
	Name          string          `json:"name"`
	Hostname      string          `json:"hostname"`
	ContainerID   json.RawMessage `json:"container_id"`
	Status        string          `json:"status"`
	LastSeen      *float64        `json:"last_seen"`
	Interval      int             `json:"interval"`
	Labels        json.RawMessage `json:"labels"`
	ThresholdsCPU *float64        `json:"thresholds_cpu"`
	ThresholdsRAM *float64        `json:"thresholds_ram"`
	DismissedAt   *int64          `json:"dismissed_at"`
	// TASK-011: system tags (JSON array; null = none). Copied from the
	// token on the first bind, synced live via PUT /tokens/{id}/tags.
	Tags []string `json:"tags"`
	// ContainerTags are admin-managed per-agent tags, independent from the
	// enrollment token's shared/system tags.
	ContainerTags     []string `json:"container_tags"`
	MonitoringIgnored bool     `json:"monitoring_ignored"`
	// explorer v4 disk: last df snapshot reported by the explorer (MiB,
	// container filesystem). null = the agent never reported it (or an
	// older explorer version is running — the fields are optional).
	DiskFreeMB  *float64     `json:"disk_free_mb"`
	DiskTotalMB *float64     `json:"disk_total_mb"`
	Metrics     *metricPoint `json:"metrics"`
	AlertCount  *int64       `json:"alert_count"`
}

// floatPtr returns nil for invalid NULL columns, the value otherwise.
func floatPtr(nf sql.NullFloat64) *float64 {
	if !nf.Valid {
		return nil
	}
	v := nf.Float64
	return &v
}

// intPtr same as floatPtr for ints.
func intPtr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// rawJSON builds the wire form of a TEXT column that stores a JSON document
// (labels, container_id as a JSON string). Invalid or empty → null.
func rawJSON(ns sql.NullString) json.RawMessage {
	s := ns.String
	if ns.Valid && s != "" {
		if json.Valid([]byte(s)) {
			return json.RawMessage(s)
		}
		// Not valid JSON (e.g. plain container hash) → embed as JSON string.
		b, err := json.Marshal(s)
		if err == nil {
			return b
		}
	}
	return nil
}

// agentScope values of GET /api/v1/agents?scope= (TASK-009): "active" (the
// default) hides soft-dismissed agents; "all" is the record view (includes
// dismissed ones with their dismissed_at field).
const (
	agentScopeActive = "active"
	agentScopeAll    = "all"
)

// GET /api/v1/agents?scope=active|all — full list: identity, status,
// last_seen, thresholds, labels (raw JSON), the LATEST sample (correlated
// MAX(ts) subquery on the (agent_id, ts) PK) and the count of unresolved
// alerts. scope defaults to "active" (dismissed agents hidden); an unknown
// scope is a 400. The frontend uses the default; scope=all exists for the
// permanent record (REQ del usuario TASK-009).
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	includeDismissed := false
	switch q := r.URL.Query().Get("scope"); q {
	case "", agentScopeActive:
	case agentScopeAll:
		includeDismissed = true
	default:
		writeError(w, http.StatusBadRequest,
			`scope must be "active" or "all"`)
		return
	}
	details, err := s.store.ListAgentsDetailed(r.Context(), includeDismissed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	out := make([]agentDTO, 0, len(details))
	for _, d := range details {
		dto := agentDTO{
			ID:                d.ID,
			Name:              d.Name,
			Hostname:          d.Hostname,
			ContainerID:       rawJSON(sql.NullString{String: d.ContainerID.String, Valid: d.ContainerID.Valid}),
			Status:            d.Status,
			LastSeen:          floatPtr(sql.NullFloat64{Float64: d.LastSeen, Valid: d.LastSeen > 0}),
			Interval:          d.Interval,
			Labels:            rawJSON(d.Labels),
			ThresholdsCPU:     floatPtr(d.ThresholdsCPU),
			ThresholdsRAM:     floatPtr(d.ThresholdsRAM),
			DismissedAt:       intPtr(d.DismissedAt),
			Tags:              tagsFromJSON(d.Tags),
			ContainerTags:     tagsFromJSON(d.ContainerTags),
			MonitoringIgnored: d.MonitoringIgnored,
			DiskFreeMB:        floatPtr(d.DiskFreeMB),
			DiskTotalMB:       floatPtr(d.DiskTotalMB),
			AlertCount:        &d.OpenAlerts,
		}
		if d.LatestTS.Valid {
			dto.Metrics = &metricPoint{
				TS:         &d.LatestTS.Int64,
				CPUPct:     floatPtr(d.CPUPct),
				RamUsedMB:  floatPtr(d.RamUsedMB),
				RamLimitMB: floatPtr(d.RamLimitMB),
				RxMB:       floatPtr(d.RxMB),
				TxMB:       floatPtr(d.TxMB),
				UptimeSec:  floatPtr(d.UptimeSec),
			}
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

// PUT /api/v1/agents/{id}/monitoring (admin) — ignore or resume monitoring
// for one discovered container, without affecting its VPS peers.
func (s *Server) handlePutAgentMonitoring(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid agent id")
		return
	}
	var body struct {
		Ignored *bool `json:"ignored"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.Ignored == nil {
		writeError(w, http.StatusBadRequest, `body must include "ignored" as a boolean`)
		return
	}
	a, err := s.store.AgentByID(r.Context(), id)
	if err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	resolved, err := s.store.SetAgentMonitoringIgnored(r.Context(), id, *body.Ignored, s.now().Unix())
	if err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		slog.Error("update container monitoring failed", "agent_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	s.broker.Broadcast("agent_monitoring_changed", map[string]any{
		"agent_id": id, "name": a.Name, "ignored": *body.Ignored,
		"alerts_resolved": resolved,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "ignored": *body.Ignored, "alerts_resolved": resolved,
	})
}

// historyPoint is one bucket of the history series. ram_limit_mb only makes
// sense per bucket when some sample in it carried a limit; buckets without
// one inherit the last known limit from earlier data (carry-forward).
type historyPoint struct {
	TS         int64    `json:"ts"`
	CPUPct     *float64 `json:"cpu_pct"`     // AVG per bucket
	RamUsedMB  *float64 `json:"ram_used_mb"` // MAX
	RamLimitMB *float64 `json:"ram_limit_mb"`
	RxMB       *float64 `json:"rx_mb"`      // MAX
	TxMB       *float64 `json:"tx_mb"`      // MAX
	UptimeSec  *float64 `json:"uptime_sec"` // MAX
}

// historyDefaults and hard caps of the range query.
const (
	defaultHistoryStep = 60   // seconds per bucket when unset
	minHistoryStep     = 30   // below this, sampling + SQLite get silly
	maxHistoryPoints   = 2000 // (to-from)/step above this → 400
)

// GET /api/v1/agents/{id}/history?from&to&step
//   - from, to: required unix seconds (payload timestamps, DSG-003)
//   - step: bucket width in seconds, default 60, clamped up to 30 min
//
// Server-side rollup SQL per REQ-018/DSG: AVG(cpu_pct) / MAX(ram_used_mb,
// rx_mb, tx_mb, uptime_sec) grouped by (ts/step)*step.
func (s *Server) handleAgentHistory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid agent id")
		return
	}
	q := r.URL.Query()
	from, err := strconv.ParseInt(q.Get("from"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "from must be an integer (unix seconds)")
		return
	}
	to, err := strconv.ParseInt(q.Get("to"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "to must be an integer (unix seconds)")
		return
	}
	if from > to {
		writeError(w, http.StatusBadRequest, "from must be <= to")
		return
	}
	step := int64(defaultHistoryStep)
	if raw := q.Get("step"); raw != "" {
		if step, err = strconv.ParseInt(raw, 10, 64); err != nil || step < 1 {
			writeError(w, http.StatusBadRequest, "step must be a positive integer (seconds)")
			return
		}
		if step < minHistoryStep {
			step = minHistoryStep
		}
	}
	if (to-from)/step > maxHistoryPoints {
		writeError(w, http.StatusBadRequest, "requested range too wide for this step")
		return
	}

	if _, err := s.store.AgentByID(r.Context(), id); err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	buckets, err := s.store.HistoryRollup(r.Context(), id, from, to, step)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	// Carry-forward of ram_limit_mb: start from the last limit known by
	// `from`, then any bucket whose own MAX limit is NULL inherits it.
	carry, err := s.store.LatestRamLimitBefore(r.Context(), id, from)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	points := make([]historyPoint, 0, len(buckets))
	for _, b := range buckets {
		if b.RamLimitMB.Valid {
			carry = b.RamLimitMB
		}
		points = append(points, historyPoint{
			TS:         b.BucketTS,
			CPUPct:     floatPtr(b.CPUPct),
			RamUsedMB:  floatPtr(b.RamUsedMB),
			RamLimitMB: floatPtr(carry),
			RxMB:       floatPtr(b.RxMB),
			TxMB:       floatPtr(b.TxMB),
			UptimeSec:  floatPtr(b.UptimeSec),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id": id,
		"step":     step,
		"points":   points,
	})
}

// thresholdDefaults match the schema defaults; they are returned when the
// agent row somehow carries NULLs.
const (
	defaultThresholdCPU = 90.0
	defaultThresholdRAM = 95.0
)

// GET /api/v1/agents/{id}/thresholds → {"thresholds_cpu","thresholds_ram"}.
func (s *Server) handleGetThresholds(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid agent id")
		return
	}
	a, err := s.store.AgentByID(r.Context(), id)
	if err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	cpu := defaultThresholdCPU
	if a.ThresholdsCPU.Valid {
		cpu = a.ThresholdsCPU.Float64
	}
	ram := defaultThresholdRAM
	if a.ThresholdsRAM.Valid {
		ram = a.ThresholdsRAM.Float64
	}
	writeThresholds(w, cpu, ram)
}

// PUT /api/v1/agents/{id}/thresholds — body {"thresholds_cpu","thresholds_ram"};
// cpu must be in [10,100], ram in [50,100] (percent). Returns the stored pair.
func (s *Server) handlePutThresholds(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid agent id")
		return
	}
	var body struct {
		ThresholdsCPU *float64 `json:"thresholds_cpu"`
		ThresholdsRAM *float64 `json:"thresholds_ram"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.ThresholdsCPU == nil || body.ThresholdsRAM == nil {
		writeError(w, http.StatusBadRequest, "thresholds_cpu and thresholds_ram are required")
		return
	}
	cpu, ram := *body.ThresholdsCPU, *body.ThresholdsRAM
	if cpu < 10 || cpu > 100 {
		writeError(w, http.StatusBadRequest, "thresholds_cpu must be between 10 and 100")
		return
	}
	if ram < 50 || ram > 100 {
		writeError(w, http.StatusBadRequest, "thresholds_ram must be between 50 and 100")
		return
	}
	if err := s.store.UpdateAgentThresholds(r.Context(), id, cpu, ram); err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeThresholds(w, cpu, ram)
}

func writeThresholds(w http.ResponseWriter, cpu, ram float64) {
	writeJSON(w, http.StatusOK, map[string]any{
		"thresholds_cpu": cpu,
		"thresholds_ram": ram,
	})
}

// DELETE /api/v1/agents/{id} — SOFT-dismiss (TASK-009, user requirement:
// baja lógica PERO con registro). The agent row is not deleted: dismissed_at
// is stamped, every OPEN alert is resolved (" — agent dado de baja"
// appended), and the agent disappears from the default list and the offline
// ticker. History/metrics/resolved alerts stay as a permanent record.
//
// Responses: 204 on success; idempotent, dismissing an already-dismissed
// agent is also 204 (no second agent_dismissed broadcast); 404 unknown id;
// 400 non-numeric id. Admin-only (router). Deliberately NO outbound
// notifier push: the dismissal was performed by the operator himself.
func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid agent id")
		return
	}
	a, err := s.store.AgentByID(r.Context(), id)
	if err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if a.DismissedAt.Valid {
		// Idempotent repeat: already dismissed, settle nothing, 204.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	now := s.now().Unix()
	_, resolved, err := s.store.DismissAgent(r.Context(), id, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	slog.Info("agent dismissed",
		"agent_id", id, "name", a.Name, "hostname", a.Hostname,
		"alerts_resolved", resolved)
	// SSE only (REQ-050 explicitly skipped for dismiss: user-driven action).
	s.broker.Broadcast("agent_dismissed", map[string]any{
		"agent_id": id, "name": a.Name,
		"status": "dismissed", "alerts_resolved": resolved,
	})
	w.WriteHeader(http.StatusNoContent)
}
