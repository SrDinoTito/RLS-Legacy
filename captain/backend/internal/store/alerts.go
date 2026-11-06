package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// InsertAlert persists one alert row (fired_at = unix seconds,
// resolved_at NULL while open).
func (s *Store) InsertAlert(ctx context.Context, a *Alert) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO alerts (agent_id, type, value, message, fired_at, resolved_at)
		 VALUES (?, ?, ?, ?, ?, NULL)`,
		a.AgentID, a.Type, a.Value, a.Message, a.FiredAt)
	if err != nil {
		return fmt.Errorf("insert alert %s for agent %d: %w", a.Type, a.AgentID, err)
	}
	a.ID, err = res.LastInsertId()
	return err
}

// CountOpenAlerts counts unresolved alerts of one type for an agent (the
// offline ticker uses it to dedupe repeat firings).
func (s *Store) CountOpenAlerts(ctx context.Context, agentID int64, typ string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM alerts
		 WHERE agent_id = ? AND type = ? AND resolved_at IS NULL`,
		agentID, typ).Scan(&n)
	return n, err
}

// ResolveAlerts closes every unresolved alert of the given type for one
// agent, stamping resolved_at, and reports how many rows it touched.
func (s *Store) ResolveAlerts(ctx context.Context, agentID int64, typ string, resolvedAtSec int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE alerts SET resolved_at = ?
		 WHERE agent_id = ? AND type = ? AND resolved_at IS NULL`,
		resolvedAtSec, agentID, typ)
	if err != nil {
		return 0, fmt.Errorf("resolve alerts %s for agent %d: %w", typ, agentID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ---- TASK-004: alerts API support ----------------------------------------

// AlertWithAgent joins an alert row with its agent display name for the
// alerts list endpoint.
type AlertWithAgent struct {
	Alert
	AgentName sql.NullString
}

// ListAlertsAll returns alerts for ALL agents (newest first), optionally
// restricted to the still-open ones (scope="active"). The agent name comes
// along so the UI can render a single flat list.
func (s *Store) ListAlertsAll(ctx context.Context, onlyOpen bool) ([]AlertWithAgent, error) {
	q := `
		SELECT al.id, al.agent_id, al.type, al.value, al.message, al.fired_at, al.resolved_at,
		       a.name
		FROM alerts al
		LEFT JOIN agents a ON a.id = al.agent_id
		WHERE 1 = 1
		ORDER BY al.id DESC`
	if onlyOpen {
		q = strings.Replace(q, "WHERE 1 = 1", `WHERE al.resolved_at IS NULL`, 1)
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list all alerts: %w", err)
	}
	defer rows.Close()
	var out []AlertWithAgent
	for rows.Next() {
		var aw AlertWithAgent
		if err := rows.Scan(&aw.ID, &aw.AgentID, &aw.Type, &aw.Value, &aw.Message,
			&aw.FiredAt, &aw.ResolvedAt, &aw.AgentName); err != nil {
			return nil, fmt.Errorf("scan alert row: %w", err)
		}
		out = append(out, aw)
	}
	return out, rows.Err()
}

// AlertByID fetches a single alert. ErrNotFound when absent.
func (s *Store) AlertByID(ctx context.Context, id int64) (*Alert, error) {
	var a Alert
	err := s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, type, value, message, fired_at, resolved_at
		 FROM alerts WHERE id = ?`, id).
		Scan(&a.ID, &a.AgentID, &a.Type, &a.Value, &a.Message, &a.FiredAt, &a.ResolvedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("alert %d: %w", id, err)
	}
	return &a, nil
}

// ResolveAlertByID closes one alert by its primary key. A no-op on an
// already-resolved alert (0 rows but no error) is not an error; the caller
// distinguishes the cases with the returned count.
func (s *Store) ResolveAlertByID(ctx context.Context, id int64, resolvedAtSec int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE alerts SET resolved_at = ? WHERE id = ? AND resolved_at IS NULL`,
		resolvedAtSec, id)
	if err != nil {
		return 0, fmt.Errorf("resolve alert %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	return n, err
}
