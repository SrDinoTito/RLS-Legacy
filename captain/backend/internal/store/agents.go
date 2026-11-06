package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Agent is a monitored explorer instance. Rows are 1:1 with bearer tokens:
// the first heartbeat carrying a token auto-registers its agent.
// DismissedAt != Invalid marks a SOFT-dismissed agent (TASK-009): the row
// is retained complete (metrics / history / resolved alerts) as a permanent
// record, but it is hidden from the active views and never fires offline
// alerts. A new heartbeat clears it (auto-restore).
type Agent struct {
	ID          int64
	TokenHash   string
	Name        string
	Hostname    string
	ContainerID sql.NullString
	Interval    int
	Status      string
	LastSeen    float64
	// TASK-004: per-agent threshold pair (percent).
	ThresholdsCPU sql.NullFloat64
	ThresholdsRAM sql.NullFloat64
	Labels        sql.NullString
	CreatedAt     int64
	// TASK-009: dismiss timestamp (unix seconds) or Invalid while active.
	DismissedAt sql.NullInt64
	// TASK-011: ecosystem tags (JSON array TEXT, e.g. ["lab","infra"]).
	// Copied from the token on the first bind and kept in sync by
	// PUT /tokens/{id}/tags. NULL = no tags.
	Tags sql.NullString
	// TASK-013: the token this agent belongs to (agents.token_id). Normal
	// 1:1 rows carry the value after the retro-fill; enrollment agents
	// (token_hash NULL) were ADOPTED by the fleet token's first beat. The
	// backlink on tokens.agent_id keeps meaning "1:1 channel".
	TokenID sql.NullInt64
	// explorer v4 disk: LAST df snapshot reported by a heartbeat (MiB,
	// filesystem "/" del container). NULL = the agent never reported it.
	// Snapshot on the agent row (like uptime/blocking metric persistence),
	// deliberately NOT part of the metrics series nor the history rollup.
	DiskFreeMB  sql.NullFloat64
	DiskTotalMB sql.NullFloat64
	// ContainerTags are operator-managed labels scoped to this container;
	// distinct from Tags, which are inherited from its enrollment token.
	ContainerTags sql.NullString
	// MonitoringIgnored excludes this container from fleet counters, metrics,
	// resource thresholds and silence alerts while retaining its record.
	MonitoringIgnored bool
}

// Metric is one ingest sample from the explorer heartbeat (ts in unix
// seconds, taken from the payload per DSG-003 so a clock-skewed agent
// still yields a consistent time series).
type Metric struct {
	AgentID    int64
	TS         int64
	CPUPct     sql.NullFloat64
	RamUsedMB  sql.NullFloat64
	RamLimitMB sql.NullFloat64
	RxMB       sql.NullFloat64
	TxMB       sql.NullFloat64
	UptimeSec  sql.NullFloat64 // TASK-004: process uptime from the payload
}

// Alert is one fired or resolved event (offline now; thresholds in TASK-004).
type Alert struct {
	ID         int64
	AgentID    int64
	Type       string
	Value      sql.NullFloat64
	Message    sql.NullString
	FiredAt    int64
	ResolvedAt sql.NullInt64
}

// agentColumns is the SELECT projection shared by every agent lookup that
// feeds scanAgent. dismissed_at rides along everywhere (TASK-009) so callers
// can tell active rows from soft-dismissed ones with a single scan helper.
const agentColumns = `id, token_hash, name, hostname, container_id, interval, status,
		       last_seen, thresholds_cpu, thresholds_ram, labels, created_at, dismissed_at, tags, token_id,
		       disk_free_mb, disk_total_mb, container_tags, monitoring_ignored`

// AgentByID looks an agent up by primary key. ErrNotFound when absent.
func (s *Store) AgentByID(ctx context.Context, id int64) (*Agent, error) {
	return scanAgent(s.db.QueryRowContext(ctx,
		`SELECT `+agentColumns+`
		 FROM agents WHERE id = ?`, id))
}

// AgentByTokenHash looks up the agent row for a token hash (1:1 mapping).
// ErrNotFound when the token has no agent row yet (never seen).
func (s *Store) AgentByTokenHash(ctx context.Context, tokenHash string) (*Agent, error) {
	return scanAgent(s.db.QueryRowContext(ctx,
		`SELECT `+agentColumns+`
		 FROM agents WHERE token_hash = ?`, tokenHash))
}

// CreateAgent inserts the auto-registered agent row for a token. The caller
// passes the resolved display name (RLS_NAME or hostname); the agent starts
// as online with last_seen = now (server time, DSG-003).
func (s *Store) CreateAgent(ctx context.Context, tokenHash, name, hostname, containerID, labels string, interval int, nowSec int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (token_hash, name, hostname, container_id, interval, status, last_seen, labels, created_at)
		 VALUES (?, ?, ?, ?, ?, 'online', ?, ?, ?)`,
		tokenHash, name, hostname, containerID, interval, nowSec, labels, nowSec)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return 0, ErrDuplicate
		}
		return 0, fmt.Errorf("insert agent: %w", err)
	}
	return res.LastInsertId()
}

// BeaconAgent upserts beacon metadata from an existing agent's heartbeat:
// name/hostname/container/labels/interval are refreshed, status forced to
// 'online' and last_seen set to server time (never the payload ts).
func (s *Store) BeaconAgent(ctx context.Context, id int64, name, hostname, containerID, labels string, interval int, nowSec int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET name = ?, hostname = ?, container_id = ?, labels = ?,
		        interval = ?, status = 'online', last_seen = ? WHERE id = ?`,
		name, hostname, containerID, labels, interval, nowSec, id)
	if err != nil {
		return fmt.Errorf("beacon agent %d: %w", id, err)
	}
	return nil
}

// UpdateAgentDisk persists the explorer v4 disk snapshot (df of "/" in MiB)
// on the agent row. COALESCE per channel: a beat that omits one of the pair
// (nil = the explorer could not collect df) keeps the previous value, so a
// partial beat never erases the last known snapshot. ErrNotFound when the
// agent does not exist.
func (s *Store) UpdateAgentDisk(ctx context.Context, id int64, free, total sql.NullFloat64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agents SET disk_free_mb = COALESCE(?, disk_free_mb),
		        disk_total_mb = COALESCE(?, disk_total_mb)
		  WHERE id = ?`,
		free, total, id)
	if err != nil {
		return fmt.Errorf("update agent %d disk: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAgentStatus flips an agent's status field (online/offline) and is
// shared by the offline ticker and future admin actions.
func (s *Store) SetAgentStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return fmt.Errorf("set agent %d status: %w", id, err)
	}
	return nil
}

// SetAgentLastSeen overwrites last_seen (unix seconds). Used by the offline
// ticker fixture paths and admin tooling; normal traffic goes through
// BeaconAgent.
func (s *Store) SetAgentLastSeen(ctx context.Context, id int64, lastSeenSec int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET last_seen = ? WHERE id = ?`, lastSeenSec, id)
	if err != nil {
		return fmt.Errorf("set agent %d last_seen: %w", id, err)
	}
	return nil
}

// SetAgentContainerTags replaces the operator-managed tags for one agent.
// Empty tags are stored as NULL; this data is independent from token tags and
// is never touched by heartbeat or token-tag synchronization.
func (s *Store) SetAgentContainerTags(ctx context.Context, id int64, tagsJSON string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agents SET container_tags = ? WHERE id = ?`, nullableText(tagsJSON), id)
	if err != nil {
		return fmt.Errorf("set agent %d container tags: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAgentMonitoringIgnored toggles monitoring for one discovered container.
// Ignoring resolves current open alerts as an operator action but retains the
// agent, metrics, history and future heartbeat identity.
func (s *Store) SetAgentMonitoringIgnored(ctx context.Context, id int64, ignored bool, nowSec int64) (resolved int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin monitoring policy for agent %d: %w", id, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	flag := 0
	if ignored {
		flag = 1
	}
	res, err := tx.ExecContext(ctx, `UPDATE agents SET monitoring_ignored = ? WHERE id = ?`, flag, id)
	if err != nil {
		return 0, fmt.Errorf("set monitoring ignored for agent %d: %w", id, err)
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return 0, fmt.Errorf("set monitoring ignored for agent %d: %w", id, rowsErr)
	} else if n == 0 {
		var exists int
		if scanErr := tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id = ?`, id).Scan(&exists); errors.Is(scanErr, sql.ErrNoRows) {
			return 0, ErrNotFound
		} else if scanErr != nil {
			return 0, fmt.Errorf("verify agent %d for monitoring policy: %w", id, scanErr)
		}
	}

	if ignored {
		res, err = tx.ExecContext(ctx,
			`UPDATE alerts SET resolved_at = ?,
			        message = CASE WHEN message LIKE '%excluido del monitoreo%'
			                       THEN message
			                       ELSE COALESCE(message, '') || ' — contenedor excluido del monitoreo'
			                  END
			 WHERE agent_id = ? AND resolved_at IS NULL`, nowSec, id)
		if err != nil {
			return 0, fmt.Errorf("resolve alerts for ignored agent %d: %w", id, err)
		}
		resolved, err = res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("count resolved alerts for ignored agent %d: %w", id, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit monitoring policy for agent %d: %w", id, err)
	}
	return resolved, nil
}

// RestoreAgent clears the soft-dismiss flag (TASK-009): a dismissed agent
// whose heartbeat returns becomes active again and re-appears in the
// default views. Idempotent (no dismissed_at → 0 rows, no error).
func (s *Store) RestoreAgent(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE agents SET dismissed_at = NULL WHERE id = ?`, id); err != nil {
		return fmt.Errorf("restore agent %d: %w", id, err)
	}
	return nil
}

// DismissAgent soft-deletes an agent (TASK-009): it stamps agents.dismissed_at
// and resolves ALL of its open alerts, appending " — agent dado de baja" to
// each resolved message (unless the suffix is already there). The row, its
// metrics and its resolved alerts stay in the database as a permanent record.
//
// Returns the effective dismissed_at and the number of alerts resolved.
//   - ErrNotFound: the agent does not exist (the API maps it to 404).
//   - already dismissed: dismissed_at = the stored value, resolved = 0, no
//     error → the DELETE endpoint stays idempotent (204 again).
func (s *Store) DismissAgent(ctx context.Context, id int64, dismissedAtSec int64) (dismissedAt int64, resolved int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("dismiss agent %d: %w", id, err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// Flipping 0 rows means "nonexistent OR already dismissed"; the SELECT
	// below tells those cases apart.
	res, err := tx.ExecContext(ctx,
		`UPDATE agents SET dismissed_at = ? WHERE id = ? AND dismissed_at IS NULL`,
		dismissedAtSec, id)
	if err != nil {
		return 0, 0, fmt.Errorf("dismiss agent %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		var stored sql.NullInt64
		selErr := tx.QueryRowContext(ctx,
			`SELECT dismissed_at FROM agents WHERE id = ?`, id).Scan(&stored)
		if errors.Is(selErr, sql.ErrNoRows) {
			return 0, 0, ErrNotFound
		}
		if selErr != nil {
			return 0, 0, fmt.Errorf("dismiss agent %d: %w", id, selErr)
		}
		if !stored.Valid {
			// UPDATED but not... impossible; defensive: report as neither.
			return 0, 0, fmt.Errorf("dismiss agent %d: inconsistent row", id)
		}
		// Idempotent repeat: nothing new to settle.
		return stored.Int64, 0, nil
	}

	key := " — agent dado de baja"
	res, err = tx.ExecContext(ctx,
		`UPDATE alerts SET resolved_at = ?,
		        message = CASE WHEN message LIKE '%dado de baja%'
		                       THEN message
		                       ELSE COALESCE(message, '') || ?
		                  END
		 WHERE agent_id = ? AND resolved_at IS NULL`,
		dismissedAtSec, key, id)
	if err != nil {
		return 0, 0, fmt.Errorf("dismiss agent %d alerts: %w", id, err)
	}
	if resolved, err = res.RowsAffected(); err != nil {
		return 0, 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("dismiss agent %d commit: %w", id, err)
	}
	return dismissedAtSec, resolved, nil
}

// ListOnlineAgents returns every DISMISSED-free agent currently marked
// online (TASK-009: dismissed agents are invisible to the offline ticker —
// they never fire offline alerts), ordered for deterministic iteration
// (oldest last_seen first — those go offline first).
func (s *Store) ListOnlineAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+agentColumns+`
		 FROM agents WHERE status = 'online' AND dismissed_at IS NULL AND monitoring_ignored = 0
		 ORDER BY last_seen ASC`)
	if err != nil {
		return nil, fmt.Errorf("list online agents: %w", err)
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// InsertMetric stores one sample. Primary key is (agent_id, ts), so a retry
// of the same payload silently replaces its own row (INSERT OR REPLACE)
// instead of failing the heartbeat.
func (s *Store) InsertMetric(ctx context.Context, m *Metric) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO metrics (agent_id, ts, cpu_pct, ram_used_mb, ram_limit_mb, rx_mb, tx_mb, uptime_sec)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.AgentID, m.TS, m.CPUPct, m.RamUsedMB, m.RamLimitMB, m.RxMB, m.TxMB, m.UptimeSec)
	if err != nil {
		return fmt.Errorf("insert metric agent %d ts %d: %w", m.AgentID, m.TS, err)
	}
	return nil
}

// CountMetricsForAgent returns the number of stored samples for one agent
// (used by integration tests to prove retries do not duplicate rows).
func (s *Store) CountMetricsForAgent(ctx context.Context, agentID int64) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM metrics WHERE agent_id = ?`, agentID).Scan(&n)
	return n, err
}

// ListAlerts returns the alerts of one agent (both open and resolved),
// newest first. Reused by the alerts API in TASK-004.
func (s *Store) ListAlerts(ctx context.Context, agentID int64) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, type, value, message, fired_at, resolved_at
		 FROM alerts WHERE agent_id = ? ORDER BY id DESC`, agentID)
	if err != nil {
		return nil, fmt.Errorf("list alerts agent %d: %w", agentID, err)
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.AgentID, &a.Type, &a.Value, &a.Message, &a.FiredAt, &a.ResolvedAt); err != nil {
			return nil, fmt.Errorf("scan alert: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAgent(row scannableRow) (*Agent, error) {
	var a Agent
	// token_hash is NULL for enrollment agents (TASK-013 1:N fleet rows)
	// so it scans through a NullString instead of a raw string.
	var tokenHash sql.NullString
	err := row.Scan(&a.ID, &tokenHash, &a.Name, &a.Hostname, &a.ContainerID, &a.Interval,
		&a.Status, &a.LastSeen, &a.ThresholdsCPU, &a.ThresholdsRAM, &a.Labels, &a.CreatedAt,
		&a.DismissedAt, &a.Tags, &a.TokenID, &a.DiskFreeMB, &a.DiskTotalMB, &a.ContainerTags, &a.MonitoringIgnored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan agent: %w", err)
	}
	a.TokenHash = tokenHash.String
	if a.Status == "" {
		a.Status = "online"
	}
	if a.Interval <= 0 {
		a.Interval = 10
	}
	return &a, nil
}

// ---- TASK-004: agents list, history rollup and thresholds ----------------

// AgentDetail is one row of the agents list: the agent row plus its latest
// sample (LEFT JOIN over the most recent ts per agent) and the count of
// unresolved alerts (badges for the UI). DismissedAt carries the
// soft-dismiss timestamp (Invalid while active; TASK-009).
type AgentDetail struct {
	Agent
	LatestTS   sql.NullInt64
	CPUPct     sql.NullFloat64
	RamUsedMB  sql.NullFloat64
	RamLimitMB sql.NullFloat64
	RxMB       sql.NullFloat64
	TxMB       sql.NullFloat64
	UptimeSec  sql.NullFloat64
	OpenAlerts int64
}

// ListAgentsDetailed returns every agent with its newest metric row and its
// count of open alerts, ordered by id. The latest sample comes from a
// correlated subquery on MAX(ts) (index-covered by the (agent_id, ts) PK).
//
// includeDismissed (TASK-009): false (default view, GET /agents) excludes
// soft-dismissed agents; true (GET /agents?scope=all) lists them too, with
// their dismissed_at populated. The dismissed ROW is never deleted, so the
// record (metrics/history/resolved alerts) remains reachable.
func (s *Store) ListAgentsDetailed(ctx context.Context, includeDismissed bool) ([]AgentDetail, error) {
	q := `
		SELECT a.id, a.name, a.hostname, a.container_id, a.status, a.last_seen, a.interval,
		       a.thresholds_cpu, a.thresholds_ram, a.labels, a.created_at, a.dismissed_at, a.tags,
	       a.disk_free_mb, a.disk_total_mb, a.container_tags, a.monitoring_ignored,
		       m.ts, m.cpu_pct, m.ram_used_mb, m.ram_limit_mb, m.rx_mb, m.tx_mb, m.uptime_sec,
		       (SELECT COUNT(*) FROM alerts al
		         WHERE al.agent_id = a.id AND al.resolved_at IS NULL) AS open_alerts
		FROM agents a
		LEFT JOIN metrics m
		       ON m.agent_id = a.id
		      AND m.ts = (SELECT MAX(ts) FROM metrics WHERE agent_id = a.id)
		ORDER BY a.id ASC`
	if !includeDismissed {
		q = strings.Replace(q, "ORDER BY a.id ASC",
			`WHERE a.dismissed_at IS NULL
		ORDER BY a.id ASC`, 1)
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list agents detailed: %w", err)
	}
	defer rows.Close()
	var out []AgentDetail
	for rows.Next() {
		var d AgentDetail
		if err := rows.Scan(
			&d.ID, &d.Name, &d.Hostname, &d.ContainerID, &d.Status, &d.LastSeen, &d.Interval,
			&d.ThresholdsCPU, &d.ThresholdsRAM, &d.Labels, &d.CreatedAt, &d.DismissedAt, &d.Tags,
			&d.DiskFreeMB, &d.DiskTotalMB, &d.ContainerTags, &d.MonitoringIgnored,
			&d.LatestTS, &d.CPUPct, &d.RamUsedMB, &d.RamLimitMB, &d.RxMB, &d.TxMB, &d.UptimeSec,
			&d.OpenAlerts,
		); err != nil {
			return nil, fmt.Errorf("scan agent detail: %w", err)
		}
		if d.Status == "" {
			d.Status = "online"
		}
		if d.Interval <= 0 {
			d.Interval = 10
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// HistoryBucket is one aggregated time bucket of the history series.
// Timestamps are bucket keys: (ts / step) * step, i.e. the bucket start.
type HistoryBucket struct {
	BucketTS   int64
	CPUPct     sql.NullFloat64 // AVG  over the samples in the bucket
	RamUsedMB  sql.NullFloat64 // MAX
	RamLimitMB sql.NullFloat64 // MAX (carry-forward applied by the caller)
	RxMB       sql.NullFloat64 // MAX
	TxMB       sql.NullFloat64 // MAX
	UptimeSec  sql.NullFloat64 // MAX
}

// HistoryRollup aggregates one agent's metrics into step-second buckets
// within [from, to]: AVG(cpu_pct) / MAX(ram_used_mb, rx_mb, tx_mb,
// uptime_sec). The (agent_id, ts) primary key covers range scans, so this
// stays cheap at v1 scale.
func (s *Store) HistoryRollup(ctx context.Context, agentID int64, from, to, step int64) ([]HistoryBucket, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT (ts / ?) * ? AS bucket,
		       AVG(cpu_pct),
		       MAX(ram_used_mb), MAX(ram_limit_mb),
		       MAX(rx_mb), MAX(tx_mb),
		       MAX(uptime_sec)
		FROM metrics
		WHERE agent_id = ? AND ts >= ? AND ts <= ?
		GROUP BY bucket
		ORDER BY bucket ASC`,
		step, step, agentID, from, to)
	if err != nil {
		return nil, fmt.Errorf("history rollup agent %d: %w", agentID, err)
	}
	defer rows.Close()
	var out []HistoryBucket
	for rows.Next() {
		var b HistoryBucket
		if err := rows.Scan(&b.BucketTS, &b.CPUPct, &b.RamUsedMB, &b.RamLimitMB,
			&b.RxMB, &b.TxMB, &b.UptimeSec); err != nil {
			return nil, fmt.Errorf("scan history bucket: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// LatestRamLimitBefore returns the newest non-null ram_limit_mb at or before
// ts for one agent (the caller forward-fills it across buckets whose
// samples lack a limit). Valid=false when no limit was ever reported.
func (s *Store) LatestRamLimitBefore(ctx context.Context, agentID int64, ts int64) (sql.NullFloat64, error) {
	var nf sql.NullFloat64
	err := s.db.QueryRowContext(ctx,
		`SELECT ram_limit_mb FROM metrics
		 WHERE agent_id = ? AND ram_limit_mb IS NOT NULL AND ts <= ?
		 ORDER BY ts DESC LIMIT 1`, agentID, ts).Scan(&nf)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.NullFloat64{}, nil
	}
	if err != nil {
		return nf, fmt.Errorf("latest ram limit agent %d: %w", agentID, err)
	}
	return nf, nil
}

// LastMetricForAgent returns the newest stored sample of one agent — the
// same "latest sample" idea ListAgentsDetailed resolves via its correlated
// subquery, as a single-row lookup for the enriched notifications. Static
// order by the (agent_id, ts) PK covers the range scan. ErrNotFound when the
// agent has no metrics (or does not exist).
func (s *Store) LastMetricForAgent(ctx context.Context, agentID int64) (*Metric, error) {
	var m Metric
	err := s.db.QueryRowContext(ctx,
		`SELECT agent_id, ts, cpu_pct, ram_used_mb, ram_limit_mb, rx_mb, tx_mb, uptime_sec
		 FROM metrics WHERE agent_id = ?
		 ORDER BY ts DESC LIMIT 1`, agentID).
		Scan(&m.AgentID, &m.TS, &m.CPUPct, &m.RamUsedMB, &m.RamLimitMB,
			&m.RxMB, &m.TxMB, &m.UptimeSec)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("last metric agent %d: %w", agentID, err)
	}
	return &m, nil
}

// UpdateAgentThresholds overwrites the high_cpu / high_ram thresholds of an
// agent. ErrNotFound when the agent does not exist.
func (s *Store) UpdateAgentThresholds(ctx context.Context, agentID int64, cpu, ram float64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agents SET thresholds_cpu = ?, thresholds_ram = ? WHERE id = ?`,
		cpu, ram, agentID)
	if err != nil {
		return fmt.Errorf("update thresholds agent %d: %w", agentID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- TASK-013: enrollment (1:N fleet tokens) ------------------------------

// AgentByName resolves the FIRST agent whose name matches (oldest first).
// Enrollment lookups rely on RLS_NAME being effectively unique; legacy
// duplicate names (2 normal tokens reporting the same one) still resolve —
// no UNIQUE index on agents.name was added on purpose (the 1:1 model never
// enforced it and a retroactive index could fail boot on existing data).
func (s *Store) AgentByName(ctx context.Context, name string) (*Agent, error) {
	return scanAgent(s.db.QueryRowContext(ctx,
		`SELECT `+agentColumns+`
		 FROM agents WHERE name = ? ORDER BY id ASC LIMIT 1`, name))
}

// CreateEnrollmentAgent inserts the auto-registered agent of an ENROLLMENT
// token's first heartbeat: 1:N model, so token_hash stays NULL (many
// agents share one token) and the link rides on agents.token_id. tagsJSON
// is the ENROLLMENT TOKEN's tags INHERITED at creation time (TASK-013
// choice: later PUT /tokens/{id}/tags does NOT re-sync adopted agents).
func (s *Store) CreateEnrollmentAgent(ctx context.Context, tokenID int64, name, hostname, containerID, labels, tagsJSON string, interval int, nowSec int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (token_hash, token_id, name, hostname, container_id, interval, status, last_seen, labels, tags, created_at)
		 VALUES (NULL, ?, ?, ?, ?, ?, 'online', ?, ?, ?, ?)`,
		tokenID, name, hostname, containerID, interval, nowSec, labels, nullableText(tagsJSON), nowSec)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return 0, ErrDuplicate
		}
		return 0, fmt.Errorf("insert enrollment agent: %w", err)
	}
	return res.LastInsertId()
}

// BindAgentTokenID stamps agents.token_id: adoption of an orphan/released
// row by an enrollment token (TASK-013). Overwrites any previous link.
func (s *Store) BindAgentTokenID(ctx context.Context, agentID, tokenID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET token_id = ? WHERE id = ?`, tokenID, agentID)
	if err != nil {
		return fmt.Errorf("bind agent %d to enrollment token %d: %w", agentID, tokenID, err)
	}
	return nil
}

// AgentIDsByTokenID lists the agent ids linked to a token via agents.token_id
// (revocation auto-dismiss of an enrollment fleet: dismiss them ALL).
func (s *Store) AgentIDsByTokenID(ctx context.Context, tokenID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM agents WHERE token_id = ? ORDER BY id ASC`, tokenID)
	if err != nil {
		return nil, fmt.Errorf("agents by token %d: %w", tokenID, err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("agents by token %d: %w", tokenID, err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
