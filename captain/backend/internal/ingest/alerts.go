// Package ingest groups the write-path helpers for explorer heartbeats and
// the background jobs that derive agent state (offline detection).
package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/rls-legacy/captain/backend/internal/store"
)

// FireOfflineAlert opens the "offline" alert of an agent unless one is still
// unresolved (dedupe: repeated ticks must not stack identical alerts).
// value = seconds since the last seen beat; message follows the wording of
// the design doc. Reusable by TASK-004 (threshold alert helpers).
func FireOfflineAlert(ctx context.Context, st *store.Store, agentID int64, value float64, now time.Time) (fired bool, err error) {
	n, err := st.CountOpenAlerts(ctx, agentID, "offline")
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil // already firing, do not duplicate
	}
	if err := st.InsertAlert(ctx, &store.Alert{
		AgentID: agentID,
		Type:    "offline",
		Value:   sql.NullFloat64{Float64: value, Valid: true},
		Message: sql.NullString{String: "sin comunicación (posible caída)", Valid: true},
		FiredAt: now.Unix(),
	}); err != nil {
		return false, err
	}
	return true, nil
}

// ResolveOfflineAlerts closes every unresolved offline alert of an agent
// (called when its heartbeat returns) and reports how many it resolved.
func ResolveOfflineAlerts(ctx context.Context, st *store.Store, agentID int64, now time.Time) (int64, error) {
	return st.ResolveAlerts(ctx, agentID, "offline", now.Unix())
}

// FireThresholdAlert opens the given threshold alert ("high_cpu" /
// "high_ram") for an agent unless one of the same type is still unresolved
// (the same dedupe contract as offline alerts: one open alert per type, no
// stacking while the breach persists). value carries the observed reading
// and message the human wording with both reading and threshold. Returns
// whether a NEW alert row was created, and that row (id included) so the
// caller can broadcast alert_new; alert is nil when fired == false.
func FireThresholdAlert(ctx context.Context, st *store.Store, agentID int64, typ string, value, threshold float64, now time.Time) (fired bool, alert *store.Alert, err error) {
	n, err := st.CountOpenAlerts(ctx, agentID, typ)
	if err != nil {
		return false, nil, err
	}
	if n > 0 {
		return false, nil, nil // breach already tracked, do not duplicate
	}
	msg := fmt.Sprintf("%s alta: valor %.1f supera umbral %.0f",
		map[string]string{"high_cpu": "CPU", "high_ram": "RAM"}[typ], value, threshold)
	created := &store.Alert{
		AgentID: agentID,
		Type:    typ,
		Value:   sql.NullFloat64{Float64: value, Valid: true},
		Message: sql.NullString{String: msg, Valid: true},
		FiredAt: now.Unix(),
	}
	if err := st.InsertAlert(ctx, created); err != nil {
		return false, nil, err
	}
	return true, created, nil
}
