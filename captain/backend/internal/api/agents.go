// agents.go: heartbeat ingest (TASK-003) plus the TASK-004 write-path
// extensions — uptime_sec persistence, per-report threshold evaluation
// (high_cpu / high_ram with dedupe) and SSE broadcast of every state
// transition. Auth is per-request Bearer token (NOT the session cookie):
// raw token → SHA-256 hex → active token lookup. The first beat of a token
// auto-registers its agent row.
package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/rls-legacy/captain/backend/internal/ingest"
	"github.com/rls-legacy/captain/backend/internal/notify"
	"github.com/rls-legacy/captain/backend/internal/store"
)

// HashAgentToken derives the storage form of a bearer token: SHA-256 hex of
// the raw value. The plaintext never touches the database.
func HashAgentToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

type ctxKeyTokenHash struct{}

// ctxKeyToken carries the resolved *store.Token row across the middleware
// boundary (TASK-013): the heartbeat handler needs it to tell normal (1:1)
// tokens from ENROLLMENT tokens (1:N fleet adoption by RLS_NAME).
type ctxKeyToken struct{}

// TASK-013 enrollment heartbeat failures (mapped to HTTP in the handler):
//   - errNameClaimed: the RLS_NAME belongs to an agent linked to ANOTHER
//     ACTIVE token → 401 (no name stealing).
//   - errNameRequired: neither the payload name nor the hostname is usable
//     → 400 (an enrollment beat without identity cannot be filed).
var (
	errNameClaimed   = errors.New("RLS_NAME claimed by an active token")
	errNameRequired  = errors.New("RLS_NAME or hostname required")
	errEnrollmentDup = errors.New("enrollment agent insert raced")
)

// requireAgentToken authenticates agent traffic via
// "Authorization: Bearer <token>". Unknown/revoked/no-header → 401.
func (s *Server) requireAgentToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
		raw = strings.TrimSpace(raw)
		if !ok || raw == "" {
			writeError(w, http.StatusUnauthorized, "invalid or revoked token")
			return
		}
		hash := HashAgentToken(raw)
		tok, err := s.store.TokenActiveByHash(r.Context(), hash)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or revoked token")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyTokenHash{}, hash)
		ctx = context.WithValue(ctx, ctxKeyToken{}, tok)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type heartbeatMetrics struct {
	CPUPct    *float64 `json:"cpu_pct"`
	RamUsedMB *float64 `json:"ram_used_mb"`
	// ram_limit_mb when reported; "max" (unlimited) is omitted upstream
	RamLimitMB *float64 `json:"ram_limit_mb"`
	RxMB       *float64 `json:"rx_mb"`
	TxMB       *float64 `json:"tx_mb"`
	UptimeSec  *float64 `json:"uptime_sec"` // TASK-004: persisted on metrics.uptime_sec
	// explorer v4 disk: df snapshot of the container filesystem (MiB).
	// Omitted by the explorer when df fails → the previous snapshot holds.
	DiskFreeMB  *float64 `json:"disk_free_mb"`
	DiskTotalMB *float64 `json:"disk_total_mb"`
}

type heartbeatRequest struct {
	Hostname    string            `json:"hostname"`
	TS          int64             `json:"ts"` // unix_ms; only used for the metrics series (DSG-003)
	Interval    int               `json:"interval"`
	Name        *string           `json:"name"`
	ContainerID *string           `json:"container_id"`
	Labels      json.RawMessage   `json:"labels"`
	Metrics     *heartbeatMetrics `json:"metrics"`
}

// validInterval clamps the reported interval into the accepted window
// 5..300 seconds; anything missing or out of range falls back to the
// documented default of 10.
func validInterval(i int) int {
	if i >= 5 && i <= 300 {
		return i
	}
	return 10
}

// handleHeartbeat — POST /api/v1/agents/heartbeat.
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	hash, _ := r.Context().Value(ctxKeyTokenHash{}).(string)
	token, _ := r.Context().Value(ctxKeyToken{}).(*store.Token)

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(body) == 0 {
		writeError(w, http.StatusBadRequest, "request body required")
		return
	}
	var req heartbeatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Metrics == nil {
		writeError(w, http.StatusBadRequest, "metrics object required")
		return
	}
	interval := validInterval(req.Interval)
	now := s.now()
	nowSec := now.Unix()

	var agent *store.Agent
	if token != nil && token.Enrollment {
		vpsID := reportedVPSID(req.Labels)
		if token.VPSScoped && strings.TrimSpace(deref(req.Name, req.Hostname)) == "" {
			writeError(w, http.StatusBadRequest, "VPS heartbeat needs a container name")
			return
		}
		if token.VPSScoped && vpsID == "" {
			writeError(w, http.StatusBadRequest, "RLS Stowaway must report the Docker Engine ID in labels.rls_vps_id")
			return
		}
		if token.VPSScoped && vpsID != "" {
			if err := s.store.BindTokenVPS(r.Context(), token.ID, vpsID); err != nil {
				switch {
				case errors.Is(err, store.ErrTokenBoundToOtherVPS):
					writeError(w, http.StatusConflict, "token is bound to a different VPS; create one token per VPS")
				case errors.Is(err, store.ErrVPSAlreadyHasToken):
					writeError(w, http.StatusConflict, "this VPS already has an active token")
				default:
					slog.Warn("VPS token binding failed", "token_id", token.ID, "vps_id", vpsID, "error", err)
					writeError(w, http.StatusInternalServerError, "database error")
				}
				return
			}
		}
		// TASK-013: fleet-enrollment token — 1:N get-or-create by RLS_NAME.
		agent, err = s.enrollmentAgent(r.Context(), token, req, interval, nowSec, now)
		switch {
		case errors.Is(err, errNameRequired):
			writeError(w, http.StatusBadRequest,
				"enrollment heartbeat needs a unique RLS_NAME (or a non-empty hostname)")
			return
		case errors.Is(err, errNameClaimed):
			// Another ACTIVE token already owns this RLS_NAME → no stealing.
			writeError(w, http.StatusUnauthorized,
				"RLS_NAME already registered by an active token")
			return
		case err != nil:
			slog.Warn("enrollment heartbeat failed",
				"token_id", token.ID, "name", deref(req.Name, ""), "error", err)
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
	} else {
		agent, err = s.store.AgentByTokenHash(r.Context(), hash)
		switch {
		case errors.Is(err, store.ErrNotFound):
			// Auto-register: this token's agent has never beaten before.
			agent, err = s.registerAgent(r.Context(), hash, req, interval, nowSec)
			if err != nil {
				if errors.Is(err, store.ErrDuplicate) {
					// Raced with a concurrent first beat of the same token:
					// fall through to the update path below.
					break
				}
				writeError(w, http.StatusInternalServerError, "database error")
				return
			}
			slog.Info("agent registered",
				"agent_id", agent.ID, "name", agent.Name, "hostname", agent.Hostname)
		case err != nil:
			writeError(w, http.StatusInternalServerError, "database error")
			return
		default:
			agent, err = s.beaconExistingAgent(r.Context(), agent, req, interval, nowSec, now)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "database error")
				return
			}
		}
	}

	// An ignored container still refreshes its identity/liveness, but contributes
	// no metrics, resource alerts, disk snapshots or offline notifications.
	if agent.MonitoringIgnored {
		s.broadcastHeartbeat(agent.ID, agent.Name, agent.Status)
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "next_interval": interval})
		return
	}

	// Metric series timestamp comes from the payload (unix milliseconds →
	// seconds) so a clock-desynced agent keeps a stable series; server time
	// is the fallback. last_seen/status above use server time (DSG-003).
	ts := nowSec
	if req.TS > 0 {
		ts = req.TS / 1000
	}
	m := &store.Metric{
		AgentID:    agent.ID,
		TS:         ts,
		CPUPct:     nullFloat(req.Metrics.CPUPct),
		RamUsedMB:  nullFloat(req.Metrics.RamUsedMB),
		RamLimitMB: nullFloat(req.Metrics.RamLimitMB),
		RxMB:       nullFloat(req.Metrics.RxMB),
		TxMB:       nullFloat(req.Metrics.TxMB),
		UptimeSec:  nullFloat(req.Metrics.UptimeSec),
	}

	// explorer v4 disk: the df snapshot persists on the agent ROW (like
	// uptime persistence it is a state, not a series — no history rollup).
	// COALESCE per channel keeps prior values for omitted fields. Best
	// effort: an UPDATE failure never denies the beat already accepted.
	if req.Metrics.DiskFreeMB != nil || req.Metrics.DiskTotalMB != nil {
		if err := s.store.UpdateAgentDisk(r.Context(), agent.ID,
			nullFloat(req.Metrics.DiskFreeMB), nullFloat(req.Metrics.DiskTotalMB)); err != nil {
			slog.Warn("agent disk update failed",
				"agent_id", agent.ID, "error", err)
		}
	}
	if err := s.store.InsertMetric(r.Context(), m); err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	// TASK-004: sustained-threshold evaluation happens on every insert. A
	// breach fires high_cpu / high_ram (deduped on open alerts); the
	// evaluation failing must not deny the already-stored sample.
	s.evalThresholds(r.Context(), agent, m, now)

	// Heartbeat event for SSE clients: minimal payload (agent id/name/status).
	s.broadcastHeartbeat(agent.ID, agent.Name, agent.Status)

	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "next_interval": interval})
}

func reportedVPSID(rawLabels json.RawMessage) string {
	var labels map[string]string
	if len(rawLabels) == 0 || json.Unmarshal(rawLabels, &labels) != nil {
		return ""
	}
	return strings.TrimSpace(labels["rls_vps_id"])
}

// beaconExistingAgent refreshes an existing agent's beacon metadata from a
// heartbeat: name/hostname/container/labels/interval, status forced online
// and last_seen = server time. Shared by the 1:1 default path and the
// TASK-013 enrollment path (both mean "this agent just beat").
// TASK-009 auto-restore: a heartbeat from a dismissed agent proves it is
// alive again → clear the soft-dismiss flag. Then, if it was offline
// (and/or dismissed), resolve its open offline alert(s) — REQ-015 (after a
// dismissal there is nothing open: DismissAgent settled everything, so
// resolved == 0 there). Broadcasts SSE + (enriched) notifier resolution.
func (s *Server) beaconExistingAgent(ctx context.Context, agent *store.Agent,
	req heartbeatRequest, interval int, nowSec int64, now time.Time) (*store.Agent, error) {
	prev := agent.Status
	wasDismissed := agent.DismissedAt.Valid
	name := deref(req.Name, agent.Name)
	if name == "" {
		name = req.Hostname
	}
	if err := s.store.BeaconAgent(ctx, agent.ID,
		name, req.Hostname, deref(req.ContainerID, ""),
		rawLabels(req.Labels), interval, nowSec); err != nil {
		return nil, err
	}
	agent.Status = "online"
	if wasDismissed {
		if err := s.store.RestoreAgent(ctx, agent.ID); err != nil {
			return nil, err
		}
		agent.DismissedAt = sql.NullInt64{}
		slog.Info("agent restored",
			"agent_id", agent.ID, "name", name, "hostname", req.Hostname)
	}
	if prev != "online" || wasDismissed {
		n, err := ingest.ResolveOfflineAlerts(ctx, s.store, agent.ID, now)
		if err != nil {
			return nil, err
		}
		s.broadcastAgentOnline(agent.ID, name, n, now.Unix())
		if n > 0 {
			slog.Info("agent back online",
				"agent_id", agent.ID, "name", name,
				"hostname", req.Hostname, "alerts_resolved", n)
			// REQ-050 enriched resolution: the agent row + the FRESH beat
			// metrics (not yet in the store) tag along. Host/container come
			// from the request — the local agent struct was fetched before
			// BeaconAgent refreshed the row on disk.
			evt := notify.Event{
				AgentID: agent.ID, AgentName: name, Type: "offline",
				Message: fmt.Sprintf("agente de nuevo en línea (%d alerta(s) offline resuelta(s))", n),
				TS:      now.Unix(), Kind: notify.KindResolved,
			}
			applyAgentTags(&evt, agent) // tags ride here (beats never touch them)
			// Host/container/interval come fresh from the request: the
			// local agent struct was read before BeaconAgent refreshed
			// the row on disk.
			evt.Hostname, evt.ContainerID, evt.Interval = req.Hostname, deref(req.ContainerID, ""), interval
			applyBeatContext(&evt, req.Metrics)
			s.enqueueAlertNotification(ctx, evt)
		}
	}
	return agent, nil
}

// enrollmentAgent resolves the agent that owns an ENROLLMENT token beat
// (TASK-013, 1:N fleet model): get-or-create by RLS_NAME.
//   - No agent with that name → auto-register (token_hash NULL — many
//     agents share the token; the link is agents.token_id) with the
//     token's tags INHERITED at creation.
//   - Name owned by THIS token (or orphaned / linked to a revoked token)
//     → adopt if needed, then beacon like any existing agent.
//   - Name owned by ANOTHER ACTIVE token → errNameClaimed (401, no
//     name stealing).
func (s *Server) enrollmentAgent(ctx context.Context, token *store.Token,
	req heartbeatRequest, interval int, nowSec int64, now time.Time) (*store.Agent, error) {
	name := deref(req.Name, "")
	if name == "" {
		name = strings.TrimSpace(req.Hostname)
	}
	if name == "" {
		return nil, errNameRequired
	}
	a, err := s.store.AgentByName(ctx, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// First beat of this fleet member: register + inherit token tags.
		id, cerr := s.store.CreateEnrollmentAgent(ctx, token.ID, name, req.Hostname,
			deref(req.ContainerID, ""), rawLabels(req.Labels),
			token.Tags.String, interval, nowSec)
		if cerr != nil {
			if errors.Is(cerr, store.ErrDuplicate) {
				// Practically impossible (no shared UNIQUE target for
				// enrollment rows): report distinctly and bail out.
				return nil, errEnrollmentDup
			}
			return nil, cerr
		}
		slog.Info("enrollment agent registered",
			"agent_id", id, "name", name, "token_id", token.ID,
			"hostname", req.Hostname)
		if a, err = s.store.AgentByID(ctx, id); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		// Ownership audit: adopt orphans/revoked-owner rows, reject names
		// still claimed by an ACTIVE token.
		if a.TokenID.Valid && a.TokenID.Int64 != token.ID {
			owner, terr := s.store.TokenByID(ctx, a.TokenID.Int64)
			switch {
			case errors.Is(terr, store.ErrNotFound):
				// Owner row gone → orphan, adopt below.
			case terr != nil:
				return nil, terr
			case owner.RevokedAt.Valid:
				// Owner revoked → the row is up for adoption.
			default:
				return nil, errNameClaimed
			}
		}
		if !a.TokenID.Valid || a.TokenID.Int64 != token.ID {
			// (Re-)adoption is a first bind: stamp the link and inherit
			// the enrollment token's tags (same rule as the 1:1 bind).
			if berr := s.store.BindAgentTokenID(ctx, a.ID, token.ID); berr != nil {
				slog.Warn("bind enrollment agent failed",
					"agent_id", a.ID, "token_id", token.ID, "error", berr)
			}
			if token.Tags.Valid && strings.TrimSpace(token.Tags.String) != "" {
				if serr := s.store.SetAgentTags(ctx, a.ID, token.Tags.String); serr != nil {
					slog.Warn("copy token tags onto adopted agent failed",
						"agent_id", a.ID, "error", serr)
				}
			}
			slog.Info("enrollment agent adopted",
				"agent_id", a.ID, "name", name, "token_id", token.ID)
		}
	}
	return s.beaconExistingAgent(ctx, a, req, interval, nowSec, now)
}

// evalThresholds compares the fresh sample against the agent's configured
// thresholds and opens a high_cpu / high_ram alert on a breach (one open
// alert per type; CountOpenAlerts dedupe, same contract as offline).
func (s *Server) evalThresholds(ctx context.Context, agent *store.Agent, m *store.Metric, now time.Time) {
	// CPU: plain comparison against thresholds_cpu.
	if m.CPUPct.Valid && agent.ThresholdsCPU.Valid && agent.ThresholdsCPU.Float64 > 0 &&
		m.CPUPct.Float64 > agent.ThresholdsCPU.Float64 {
		s.fireThreshold(ctx, agent, m, "high_cpu", m.CPUPct.Float64, agent.ThresholdsCPU.Float64, now)
	}

	// RAM: used/limit ratio in percent against thresholds_ram.
	if m.RamUsedMB.Valid && m.RamLimitMB.Valid && m.RamLimitMB.Float64 > 0 &&
		agent.ThresholdsRAM.Valid && agent.ThresholdsRAM.Float64 > 0 {
		pct := m.RamUsedMB.Float64 / m.RamLimitMB.Float64 * 100
		if pct > agent.ThresholdsRAM.Float64 {
			s.fireThreshold(ctx, agent, m, "high_ram", pct, agent.ThresholdsRAM.Float64, now)
		}
	}
}

// fireThreshold invokes the ingest helper (dedupe included). Errors are
// logged but never fail the heartbeat that carried the sample. When a NEW
// alert row is created, an alert_new event is broadcast (and notified,
// enriched: the triggering sample rides along).
func (s *Server) fireThreshold(ctx context.Context, agent *store.Agent, m *store.Metric, typ string, value, threshold float64, now time.Time) {
	fired, alert, err := ingest.FireThresholdAlert(ctx, s.store, agent.ID, typ, value, threshold, now)
	switch {
	case err != nil:
		slog.Error("threshold alert failed", "agent_id", agent.ID, "type", typ, "error", err)
		return
	case !fired:
		return // breach already tracked by an open alert
	}
	slog.Warn("threshold alert fired",
		"agent_id", agent.ID, "name", agent.Name, "type", typ,
		"value", value, "threshold", threshold)
	s.broker.Broadcast("alert_new", map[string]any{
		"id": alert.ID, "agent_id": agent.ID, "type": typ,
		"value": value, "message": alert.Message.String, "fired_at": alert.FiredAt,
	})
	// REQ-050: same transition, out-of-band notification (best-effort),
	// fully enriched — the agent row and the triggering sample are in hand.
	s.notifyAgentEvent(ctx, notify.Event{
		AgentID:   agent.ID,
		AgentName: agent.Name,
		Type:      typ,
		Value:     value,
		Message:   alert.Message.String,
		TS:        alert.FiredAt,
		Kind:      notify.KindNew,
	}, agent, m)
}

// broadcastHeartbeat emits the heartbeat transition (agent id/name/status
// only — metric details come from the normal polling/API path).
func (s *Server) broadcastHeartbeat(agentID int64, name, status string) {
	s.broker.Broadcast("heartbeat", map[string]any{
		"agent_id": agentID, "name": name, "status": status,
	})
}

// broadcastAgentOnline emits agent_online plus alert_resolved for the
// offline alert(s) that the resurrection just closed. (The REQ-050
// resolution notification lives in the heartbeat handler: it carries the
// fresh beat metrics, which only that site knows.)
func (s *Server) broadcastAgentOnline(agentID int64, name string, resolved int64, resolvedAt int64) {
	s.broker.Broadcast("agent_online", map[string]any{
		"agent_id": agentID, "name": name, "status": "online",
	})
	if resolved > 0 {
		s.broker.Broadcast("alert_resolved", map[string]any{
			"agent_id": agentID, "type": "offline", "count": resolved,
		})
	}
}

// registerAgent inserts the agent row for a token's first heartbeat
// (status='online', last_seen=server time) and links it on the token row,
// completing the 1:1 token↔agent mapping.
func (s *Server) registerAgent(ctx context.Context, hash string, req heartbeatRequest, interval int, nowSec int64) (*store.Agent, error) {
	name := deref(req.Name, "")
	if name == "" {
		name = req.Hostname
	}
	agentID, err := s.store.CreateAgent(ctx, hash, name, req.Hostname,
		deref(req.ContainerID, ""), rawLabels(req.Labels), interval, nowSec)
	if err != nil {
		return nil, err
	}
	if token, err := s.store.TokenActiveByHash(ctx, hash); err == nil {
		// Best-effort backlink; failure does not deny the beat itself.
		if err := s.store.BindTokenAgent(ctx, token.ID, agentID); err != nil {
			slog.Warn("bind agent to token failed", "agent_id", agentID, "error", err)
		}
		// TASK-013: agents.token_id mirrors the 1:1 link (the boot
		// retro-fill can only cover rows that existed before startup).
		if err := s.store.BindAgentTokenID(ctx, agentID, token.ID); err != nil {
			slog.Warn("stamp agents.token_id failed", "agent_id", agentID, "error", err)
		}
		// TASK-011 (DEC-011): first bind — the token's system tags propagate
		// to the agent (ecosystem tags shared token↔agent). Later beats do
		// NOT re-touch the tags (BeaconAgent never writes agents.tags);
		// live edits flow through PUT /tokens/{id}/tags.
		if token.Tags.Valid && strings.TrimSpace(token.Tags.String) != "" {
			if err := s.store.SetAgentTags(ctx, agentID, token.Tags.String); err != nil {
				slog.Warn("copy token tags to agent failed",
					"agent_id", agentID, "error", err)
			}
		}
	}
	return s.store.AgentByID(ctx, agentID)
}

func deref(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return strings.TrimSpace(*p)
}

func rawLabels(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return string(raw)
}

func nullFloat(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}
