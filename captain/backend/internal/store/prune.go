package store

import (
	"context"
	"fmt"
)

// PruneResult reports the rows removed by one retention sweep (TASK-008, REQ-040).
type PruneResult struct {
	Metrics int64
	Alerts  int64
}

// Prune deletes every metric sample strictly older than beforeSec (unix
// seconds) and every RESOLVED alert whose resolved_at is beyond the same
// cutoff. Open (unresolved) alerts are never deleted, so an offline agent
// whose history was already dropped keeps firing while it is silent.
//
// The (agent_id, ts) primary key makes the metrics range DELETE cheap at
// v1 scale; returns the actual affected row counts for the job's log line.
func (s *Store) Prune(ctx context.Context, beforeSec int64) (PruneResult, error) {
	var out PruneResult

	res, err := s.db.ExecContext(ctx, `DELETE FROM metrics WHERE ts < ?`, beforeSec)
	if err != nil {
		return out, fmt.Errorf("prune metrics older than %d: %w", beforeSec, err)
	}
	out.Metrics, err = res.RowsAffected()
	if err != nil {
		return out, fmt.Errorf("prune metrics rows affected: %w", err)
	}

	res, err = s.db.ExecContext(ctx,
		`DELETE FROM alerts WHERE resolved_at IS NOT NULL AND resolved_at < ?`, beforeSec)
	if err != nil {
		return out, fmt.Errorf("prune alerts resolved before %d: %w", beforeSec, err)
	}
	out.Alerts, err = res.RowsAffected()
	if err != nil {
		return out, fmt.Errorf("prune alerts rows affected: %w", err)
	}

	return out, nil
}
