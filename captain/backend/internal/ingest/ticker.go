// Ticker: background job that flips silent agents to offline. REQ-015:
// every ~10s, any agent whose last_seen is older than 3 × its reported
// interval is marked 'offline' and gets an offline alert (dedup); the
// resurrection path lives in the heartbeat handler (api package).
package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/rls-legacy/captain/backend/internal/notify"
	"github.com/rls-legacy/captain/backend/internal/sse"
	"github.com/rls-legacy/captain/backend/internal/store"
)

// DefaultTickInterval is the REQ-041 detection cadence; configurable via
// RLS_OFFLINE_TICK_SEC (env) for testability.
const DefaultTickInterval = 10 * time.Second

// Ticker periodically evaluates every online agent against its own interval.
type Ticker struct {
	st       *store.Store
	every    time.Duration
	now      func() time.Time // injectable clock (tests)
	log      *slog.Logger
	sse      *sse.Broker      // nil-safe: no broker → no broadcasting
	notifier *notify.Notifier // nil-safe: no notifier → no alert_new push (REQ-050)
}

// NewTicker builds a ticker over the given store. every <= 0 falls back to
// DefaultTickInterval. Extras (variadic optional, matched by type) are the
// *sse.Broker (TASK-004) and the *notify.Notifier (REQ-050) so main and the
// tests can share the same instances with the API server.
func NewTicker(st *store.Store, every time.Duration, extras ...any) *Ticker {
	if every <= 0 {
		every = DefaultTickInterval
	}
	var b *sse.Broker
	var n *notify.Notifier
	for _, x := range extras {
		switch one := x.(type) {
		case *sse.Broker:
			if one != nil {
				b = one
			}
		case *notify.Notifier:
			if one != nil {
				n = one
			}
		}
	}
	return &Ticker{st: st, every: every, now: time.Now, log: slog.Default(), sse: b, notifier: n}
}

// SetNow overrides the clock (tests only).
func (t *Ticker) SetNow(f func() time.Time) { t.now = f }

// Tick performs a single offline sweep and returns how many agents were
// flipped offline. It is exported so tests (TASK-003 e2e) can drive the
// transition deterministically instead of sleeping on the goroutine.
func (t *Ticker) Tick(ctx context.Context) (int, error) {
	agents, err := t.st.ListOnlineAgents(ctx)
	if err != nil {
		return 0, fmt.Errorf("offline tick: %w", err)
	}
	if len(agents) == 0 {
		return 0, nil
	}
	now := t.now()
	marked := 0
	for _, a := range agents {
		interval := float64(a.Interval)
		if interval <= 0 {
			interval = 10 // matches schema default
		}
		age := now.Unix() - int64(a.LastSeen)
		if float64(age) <= 3*interval {
			continue
		}
		if err := t.st.SetAgentStatus(ctx, a.ID, "offline"); err != nil {
			return marked, err
		}
		fired, err := FireOfflineAlert(ctx, t.st, a.ID, float64(age), now)
		if err != nil {
			return marked, err
		}
		marked++
		t.log.Info("agent offline",
			"agent_id", a.ID, "name", a.Name, "agent", a.Hostname,
			"last_seen_age_sec", age, "interval_sec", a.Interval,
			"alert_fired", fired)
		t.broadcastOffline(a)
		// REQ-050: notify the NEW offline alert only (dedupe keeps the
		// message accurate — an already-open alert is not news). Enriched:
		// the agent row is in hand and the last stored sample is fetched
		// best-effort (a never-beaten agent notifies without metric lines).
		if fired {
			var m *store.Metric
			if last, err := t.st.LastMetricForAgent(ctx, a.ID); err == nil {
				m = last
			}
			t.notifyAlertNew(ctx, a, m, float64(age), now)
		}
	}
	return marked, nil
}

// broadcastOffline emits the agent_offline event when a broker is wired.
// Best-effort by construction (Broadcast never errors); the offline flip
// above is already committed and must not be retried on broadcast issues.
func (t *Ticker) broadcastOffline(a store.Agent) {
	if t.sse == nil {
		return
	}
	t.sse.Broadcast("agent_offline", map[string]any{
		"agent_id": a.ID, "name": a.Name, "status": a.Status,
	})
}

// notifyAlertNew pushes the freshly fired offline alert into the outbound
// notifier (same wording the alert row stores), enriched with the agent
// context and its last sample (metric lookup lives with the caller, where
// the request context is available). Best-effort: Enqueue never blocks and
// a nil notifier means "not configured".
func (t *Ticker) notifyAlertNew(ctx context.Context, a store.Agent, m *store.Metric, value float64, now time.Time) {
	if t.notifier == nil {
		return
	}
	evt := notify.Event{
		AgentID:     a.ID,
		AgentName:   a.Name,
		Type:        "offline",
		Value:       value,
		Message:     "sin comunicación (posible caída)",
		TS:          now.Unix(),
		Kind:        notify.KindNew,
		Tags:        store.ParseTags(a.Tags),
		Hostname:    a.Hostname,
		ContainerID: a.ContainerID.String,
		Interval:    a.Interval,
	}
	if m != nil {
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
	allowed, err := t.st.ShouldNotifyAlert(ctx, a.ID, evt.Type)
	if err != nil {
		t.log.Error("notification policy lookup failed; outbound event suppressed",
			"agent_id", a.ID, "type", evt.Type, "error", err)
		return
	}
	if !allowed {
		t.log.Info("outbound notification suppressed by policy", "agent_id", a.ID, "type", evt.Type)
		return
	}
	t.notifier.Enqueue(evt)
}

// Run sweeps every t.every until ctx is cancelled. It fires one sweep
// immediately on startup, then on each tick. Returns when the loop ends so
// main can await it synchronously during graceful shutdown.
func (t *Ticker) Run(ctx context.Context) {
	ticker := time.NewTicker(t.every)
	defer ticker.Stop()
	_, err := t.Tick(ctx)
	if err != nil {
		t.log.Error("offline tick failed", "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			t.log.Info("offline ticker stopped")
			return
		case <-ticker.C:
			if _, err := t.Tick(ctx); err != nil {
				t.log.Error("offline tick failed", "error", err)
			}
		}
	}
}
