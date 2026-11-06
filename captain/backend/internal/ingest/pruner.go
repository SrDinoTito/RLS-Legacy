// Pruner: background retention job (REQ-040, TASK-008). Every hour (by
// default, RLS_PRUNE_TICK_SEC for tests) it drops metric samples and
// resolved alerts older than RLS_RETENTION_DAYS from the SQLite volume, so
// a long-lived deployment never grows unbounded and stays lightweight
// (DEC-006). Run() mirrors the offline Ticker: cancellable via ctx so
// main.go can await it synchronously during graceful shutdown.
package ingest

import (
	"context"
	"log/slog"
	"time"

	"github.com/rls-legacy/captain/backend/internal/store"
)

// DefaultPruneInterval is the REQ-040 pruning cadence (hourly); the
// retention window itself is a separate knob (RLS_RETENTION_DAYS, default 7).
const DefaultPruneInterval = 1 * time.Hour

// Pruner periodically applies the retention cutoff over metrics + alerts.
type Pruner struct {
	st        *store.Store
	every     time.Duration
	retention int64            // retention window in days
	now       func() time.Time // injectable clock (tests)
	log       *slog.Logger
}

// NewPruner builds a Pruner over the given store. every <= 0 falls back to
// DefaultPruneInterval; retentionDays <= 0 falls back to 7 (REQ-040).
func NewPruner(st *store.Store, every time.Duration, retentionDays int64) *Pruner {
	if every <= 0 {
		every = DefaultPruneInterval
	}
	if retentionDays <= 0 {
		retentionDays = 7
	}
	return &Pruner{st: st, every: every, retention: retentionDays, now: time.Now, log: slog.Default()}
}

// SetNow overrides the clock (tests only).
func (p *Pruner) SetNow(f func() time.Time) { p.now = f }

// Tick performs a single retention sweep and reports the removed rows.
// Exported like Ticker.Tick so tests drive it deterministically.
func (p *Pruner) Tick(ctx context.Context) (store.PruneResult, error) {
	before := p.now().Unix() - p.retention*86400
	res, err := p.st.Prune(ctx, before)
	if err != nil {
		return res, err
	}
	p.log.Info("retention pruned",
		"before_unix", before, "retention_days", p.retention,
		"metrics", res.Metrics, "alerts", res.Alerts)
	return res, nil
}

// Run sweeps every p.every until ctx is cancelled. One immediate sweep on
// startup, then per tick; exits when told so main can join it on shutdown.
func (p *Pruner) Run(ctx context.Context) {
	ticker := time.NewTicker(p.every)
	defer ticker.Stop()
	if _, err := p.Tick(ctx); err != nil {
		p.log.Error("retention prune failed", "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			p.log.Info("retention pruner stopped")
			return
		case <-ticker.C:
			if _, err := p.Tick(ctx); err != nil {
				p.log.Error("retention prune failed", "error", err)
			}
		}
	}
}
