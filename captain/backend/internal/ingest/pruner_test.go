// TASK-008 (REQ-040): the retention pruner removes metric samples and
// resolved alerts older than the retention window, leaving the newest data
// untouched. Old + new inserted → only the new survives the sweep.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"testing"
	"time"

	"github.com/rls-legacy/captain/backend/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seedAgent(t *testing.T, st *store.Store) int64 {
	t.Helper()
	sum := sha256.Sum256([]byte("e2e-token"))
	hash := hex.EncodeToString(sum[:])
	id, err := st.CreateAgent(context.Background(), hash, "e2e", "host-1", "", "", 10, time.Now().Unix())
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	return id
}

func TestPrunerTickRemovesOldData(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	agentID := seedAgent(t, st)

	now := time.Now()
	day := int64(86400)
	oldTS := now.Unix() - 3*day // 3 days old: beyond the 1-day window
	newTS := now.Unix()

	for _, ts := range []int64{oldTS, newTS} {
		m := store.Metric{AgentID: agentID, TS: ts}
		if err := st.InsertMetric(ctx, &m); err != nil {
			t.Fatalf("insert metric %d: %v", ts, err)
		}
	}

	// One resolved alert beyond the window and one inside it. InsertAlert
	// creates open rows, so the old one is resolved explicitly at oldTS.
	for _, ended := range []int64{oldTS, newTS} {
		a := &store.Alert{AgentID: agentID, Type: "high_cpu", FiredAt: ended}
		if err := st.InsertAlert(ctx, a); err != nil {
			t.Fatalf("insert alert: %v", err)
		}
		if _, err := st.ResolveAlertByID(ctx, a.ID, ended); err != nil {
			t.Fatalf("resolve alert %d: %v", a.ID, err)
		}
	}

	p := NewPruner(st, time.Hour, 1) // retention = 1 day
	p.SetNow(func() time.Time { return now })
	p.log = slog.New(slog.DiscardHandler)

	res, err := p.Tick(ctx)
	if err != nil {
		t.Fatalf("pruner tick: %v", err)
	}
	if res.Metrics != 1 || res.Alerts != 1 {
		t.Fatalf("pruned wrong counts: %+v (want 1 metric, 1 alert)", res)
	}

	// The fresh sample and the fresh alert must be the only rows left.
	if n, err := st.CountMetricsForAgent(ctx, agentID); err != nil || n != 1 {
		t.Fatalf("metrics left = %d (want 1): %v", n, err)
	}
	all, err := st.ListAlertsAll(ctx, false)
	if err != nil {
		t.Fatalf("list alerts: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("alerts left = %d rows (want 1)", len(all))
	}

	// Open alerts must never be dropped by a sweep, however old.
	if _, err := p.Tick(ctx); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if n, _ := st.CountOpenAlerts(ctx, agentID, "offline"); n != 0 {
		t.Fatalf("offline open alert appeared unexpectedly")
	}
}

func TestPrunerDefaults(t *testing.T) {
	// Zero/negative values fall back to safer production defaults.
	p := NewPruner(newTestStore(t), 0, 0)
	if p.every != DefaultPruneInterval {
		t.Fatalf("default interval wrong: %v", p.every)
	}
	if p.retention != 7 {
		t.Fatalf("default retention wrong: %d", p.retention)
	}
}
