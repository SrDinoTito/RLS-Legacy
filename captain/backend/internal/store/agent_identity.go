package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

type duplicateAgentIdentity struct {
	tokenID int64
	key     string
}

// mergeDuplicateAgentIdentities upgrades databases created before Docker
// runtime IDs were separated from logical container identities. It keeps the
// most recently active row, moves its metrics/alerts/settings, and deletes
// redundant rows before the unique identity index is installed.
func mergeDuplicateAgentIdentities(ctx context.Context, db *sql.DB, nowSec int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin identity merge: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT token_id, identity_key FROM agents
		 WHERE token_id IS NOT NULL AND identity_key IS NOT NULL
		 GROUP BY token_id, identity_key HAVING COUNT(*) > 1`)
	if err != nil {
		return fmt.Errorf("list duplicate identities: %w", err)
	}
	var groups []duplicateAgentIdentity
	for rows.Next() {
		var group duplicateAgentIdentity
		if err := rows.Scan(&group.tokenID, &group.key); err != nil {
			rows.Close()
			return fmt.Errorf("scan duplicate identity: %w", err)
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate duplicate identities: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close duplicate identities: %w", err)
	}

	for _, group := range groups {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, status, last_seen FROM agents
			 WHERE token_id = ? AND identity_key = ?
			 ORDER BY last_seen DESC,
			          CASE WHEN dismissed_at IS NULL THEN 0 ELSE 1 END,
			          CASE WHEN status = 'online' THEN 0 ELSE 1 END,
			          id DESC`, group.tokenID, group.key)
		if err != nil {
			return fmt.Errorf("load agents for token %d identity %q: %w", group.tokenID, group.key, err)
		}
		type row struct {
			id       int64
			status   string
			lastSeen sql.NullFloat64
		}
		var agents []row
		for rows.Next() {
			var agent row
			if err := rows.Scan(&agent.id, &agent.status, &agent.lastSeen); err != nil {
				rows.Close()
				return fmt.Errorf("scan agents for token %d identity %q: %w", group.tokenID, group.key, err)
			}
			agents = append(agents, agent)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate agents for token %d identity %q: %w", group.tokenID, group.key, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close agents for token %d identity %q: %w", group.tokenID, group.key, err)
		}
		if len(agents) < 2 {
			continue
		}

		canonical := agents[0]
		for _, duplicate := range agents[1:] {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO metrics (agent_id, ts, cpu_pct, ram_used_mb, ram_limit_mb, rx_mb, tx_mb, uptime_sec)
				 SELECT ?, ts, cpu_pct, ram_used_mb, ram_limit_mb, rx_mb, tx_mb, uptime_sec
				 FROM metrics WHERE agent_id = ?`, canonical.id, duplicate.id); err != nil {
				return fmt.Errorf("merge metrics from agent %d into %d: %w", duplicate.id, canonical.id, err)
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE alerts SET agent_id = ? WHERE agent_id = ?`, canonical.id, duplicate.id); err != nil {
				return fmt.Errorf("merge alerts from agent %d into %d: %w", duplicate.id, canonical.id, err)
			}
			oldID, newID := strconv.FormatInt(duplicate.id, 10), strconv.FormatInt(canonical.id, 10)
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO notification_policies (scope, scope_id, notifications_enabled, resource_alerts_enabled, updated_at)
				 SELECT scope, ?, notifications_enabled, resource_alerts_enabled, updated_at
				 FROM notification_policies WHERE scope = 'agent' AND scope_id = ?`, newID, oldID); err != nil {
				return fmt.Errorf("merge notification policy from agent %d into %d: %w", duplicate.id, canonical.id, err)
			}
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM notification_policies WHERE scope = 'agent' AND scope_id = ?`, oldID); err != nil {
				return fmt.Errorf("remove duplicate notification policy for agent %d: %w", duplicate.id, err)
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE tokens SET agent_id = ? WHERE agent_id = ?`, canonical.id, duplicate.id); err != nil {
				return fmt.Errorf("rebind token backlink from agent %d to %d: %w", duplicate.id, canonical.id, err)
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE agents SET monitoring_ignored = MAX(monitoring_ignored, (SELECT monitoring_ignored FROM agents WHERE id = ?))
				 WHERE id = ?`, duplicate.id, canonical.id); err != nil {
				return fmt.Errorf("merge ignored preference from agent %d into %d: %w", duplicate.id, canonical.id, err)
			}
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM metrics WHERE agent_id = ?`, duplicate.id); err != nil {
				return fmt.Errorf("remove duplicate metrics for agent %d: %w", duplicate.id, err)
			}
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM agents WHERE id = ?`, duplicate.id); err != nil {
				return fmt.Errorf("remove duplicate agent %d: %w", duplicate.id, err)
			}
		}

		if canonical.status == "online" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE alerts SET resolved_at = ?
				 WHERE agent_id = ? AND resolved_at IS NULL AND type IN ('offline', 'container_missing')`, nowSec, canonical.id); err != nil {
				return fmt.Errorf("resolve stale lifecycle alerts for agent %d: %w", canonical.id, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE alerts SET resolved_at = ?
			 WHERE agent_id = ? AND resolved_at IS NULL
			   AND id NOT IN (SELECT MAX(id) FROM alerts WHERE agent_id = ? AND resolved_at IS NULL GROUP BY type)`,
			nowSec, canonical.id, canonical.id); err != nil {
			return fmt.Errorf("deduplicate open alerts for agent %d: %w", canonical.id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit identity merge: %w", err)
	}
	return nil
}
