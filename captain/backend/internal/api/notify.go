// notify.go: admin test endpoint for the notification channels (REQ-050).
// POST /api/v1/notify/test {channel:"discord"|"telegram"} → sends the
// "RLS test ok" message synchronously so the caller gets the real outcome:
// 201 ok, 503 channel not configured (or nothing configured at all), 502
// when the channel refused the delivery, 400 on an unknown channel.
// Scheduled alert notifications themselves are async (the notifier worker).
//
// The Enqueue helpers below mirror every alert-emission site and enrich the
// notification payload: whatever the caller already holds (the agent row,
// the triggering sample) is passed directly; anything else (manual resolve,
// ticker) is looked up best-effort. Absent data degrades to omitted fields,
// never to a failed alert.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/rls-legacy/captain/backend/internal/notify"
	"github.com/rls-legacy/captain/backend/internal/store"
)

type notifyTestRequest struct {
	Channel string `json:"channel"`
}

// POST /api/v1/notify/test — admin-only (registrar in the admin group).
func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	if s.notifier == nil {
		writeError(w, http.StatusServiceUnavailable,
			"no notification channels configured (set RLS_NOTIF_* envs)")
		return
	}
	var req notifyTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	switch req.Channel {
	case "discord", "telegram":
	default:
		writeError(w, http.StatusBadRequest,
			`channel must be "discord" or "telegram"`)
		return
	}
	if err := s.notifier.SendTest(r.Context(), req.Channel); err != nil {
		if errors.Is(err, notify.ErrNotConfigured) {
			writeError(w, http.StatusServiceUnavailable,
				"channel "+req.Channel+" is not configured (set its RLS_NOTIF_* envs)")
			return
		}
		writeError(w, http.StatusBadGateway,
			"notify test failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"status": "ok", "channel": req.Channel,
	})
}

// ---- best-effort Enqueue helpers (mirror of every broadcast site) --------

// notifyAgentEvent enqueues an alert notification enriched from the agent
// row and metric the caller already holds (threshold breach site: the fresh
// sample is right there; resurrection: the incoming beat). Either row may
// be nil — its fields are then simply omitted. Never blocks, never errors.
func (s *Server) notifyAgentEvent(ctx context.Context, evt notify.Event, a *store.Agent, m *store.Metric) {
	if s.notifier == nil {
		return
	}
	if a != nil {
		applyAgentContext(&evt, a)
	}
	if m != nil {
		applyMetricContext(&evt, m)
	}
	s.enqueueAlertNotification(ctx, evt)
}

// notifyLookupEvent enqueues an alert notification for the sites holding
// only the alert row (manual resolve): the agent context (name, tags,
// hostname, container, interval) and its latest sample are looked up
// best-effort — a purged agent just degrades the enrichment to omitted.
func (s *Server) notifyLookupEvent(ctx context.Context, evt notify.Event) {
	if s.notifier == nil {
		return
	}
	if evt.AgentID > 0 {
		if a, err := s.store.AgentByID(ctx, evt.AgentID); err == nil {
			applyAgentContext(&evt, a)
			if evt.AgentName == "" {
				evt.AgentName = a.Name
			}
		}
		if m, err := s.store.LastMetricForAgent(ctx, evt.AgentID); err == nil {
			applyMetricContext(&evt, m)
		}
	}
	s.enqueueAlertNotification(ctx, evt)
}

// enqueueAlertNotification applies persisted per-VPS/per-container policy
// before any external channel sees an event. Fail closed on policy read
// errors; the in-app alert and SSE broadcast have already been committed.
func (s *Server) enqueueAlertNotification(ctx context.Context, evt notify.Event) {
	if s.notifier == nil {
		return
	}
	allowed, err := s.store.ShouldNotifyAlert(ctx, evt.AgentID, evt.Type)
	if err != nil {
		slog.Error("notification policy lookup failed; outbound event suppressed",
			"agent_id", evt.AgentID, "type", evt.Type, "error", err)
		return
	}
	if !allowed {
		slog.Info("outbound notification suppressed by policy",
			"agent_id", evt.AgentID, "type", evt.Type)
		return
	}
	s.notifier.Enqueue(evt)
}

// applyAgentContext fills the agent-context enrichment from a row.
func applyAgentContext(evt *notify.Event, a *store.Agent) {
	applyAgentTags(evt, a)
	evt.Hostname = a.Hostname
	evt.ContainerID = a.ContainerID.String
	evt.Interval = a.Interval
}

// applyAgentTags fills only the ecosystem tags (resurrection site: the rest
// of the agent fields come fresh from the request payload).
func applyAgentTags(evt *notify.Event, a *store.Agent) {
	evt.Tags = store.ParseTags(a.Tags)
}

// applyMetricContext fills the latest-sample enrichment from a stored row.
func applyMetricContext(evt *notify.Event, m *store.Metric) {
	if m.CPUPct.Valid {
		v := m.CPUPct.Float64
		evt.LastCPU = &v
	}
	if m.RamUsedMB.Valid {
		v := m.RamUsedMB.Float64
		evt.LastRamUsed = &v
	}
	if m.RamLimitMB.Valid {
		v := m.RamLimitMB.Float64
		evt.LastRamLim = &v
	}
	if m.UptimeSec.Valid {
		v := int64(m.UptimeSec.Float64)
		evt.UptimeSec = &v
	}
}

// applyBeatContext fills the sample enrichment straight from the heartbeat
// payload — used by the resurrection site, where the fresh sample is known
// but not yet in the store (the insert happens right after).
func applyBeatContext(evt *notify.Event, hm *heartbeatMetrics) {
	if hm == nil {
		return
	}
	if hm.CPUPct != nil {
		v := *hm.CPUPct
		evt.LastCPU = &v
	}
	if hm.RamUsedMB != nil {
		v := *hm.RamUsedMB
		evt.LastRamUsed = &v
	}
	if hm.RamLimitMB != nil {
		v := *hm.RamLimitMB
		evt.LastRamLim = &v
	}
	if hm.UptimeSec != nil {
		v := int64(*hm.UptimeSec)
		evt.UptimeSec = &v
	}
}
