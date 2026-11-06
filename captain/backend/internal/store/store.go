// Package store encapsulates all SQLite persistence for the captain backend.
// It uses modernc.org/sqlite (pure Go, no cgo) with WAL journaling and
// foreign keys enabled via DSN pragmas so they apply to every pooled
// connection, not just the first one.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// ErrDuplicate is returned by insert methods when a UNIQUE constraint fails.
var ErrDuplicate = errors.New("duplicate value")

// schema is the embedded migration. It is idempotent: every statement uses
// CREATE TABLE/INDEX IF NOT EXISTS, so it is safe to run on every startup.
const schema = `
CREATE TABLE IF NOT EXISTS users (
	id INTEGER PRIMARY KEY,
	username TEXT UNIQUE NOT NULL,
	password_hash TEXT NOT NULL,
	role TEXT NOT NULL CHECK (role IN ('admin','viewer')),
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS agents (
	id INTEGER PRIMARY KEY,
	token_hash TEXT UNIQUE,
	name TEXT,
	hostname TEXT,
	container_id TEXT,
	interval INTEGER DEFAULT 10,
	status TEXT DEFAULT 'online',
	last_seen REAL,
	thresholds_cpu REAL DEFAULT 90,
	thresholds_ram REAL DEFAULT 95,
	labels TEXT,
	created_at INTEGER,
	monitoring_ignored INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS metrics (
	agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	ts INTEGER NOT NULL,
	cpu_pct REAL,
	ram_used_mb REAL,
	ram_limit_mb REAL,
	rx_mb REAL,
	tx_mb REAL,
	PRIMARY KEY (agent_id, ts)
);
CREATE TABLE IF NOT EXISTS alerts (
	id INTEGER PRIMARY KEY,
	agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	type TEXT NOT NULL,
	value REAL,
	message TEXT,
	fired_at INTEGER NOT NULL,
	resolved_at INTEGER
);
CREATE TABLE IF NOT EXISTS tokens (
	id INTEGER PRIMARY KEY,
	token_hash TEXT UNIQUE,
	name TEXT NOT NULL,
	agent_id INTEGER,
	created_at INTEGER NOT NULL,
	revoked_at INTEGER,
	vps_id TEXT,
	vps_scoped INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_agents_last_seen ON agents(last_seen);

-- Notification channels configured from the dashboard (REQ-050): a single
-- settings row (id=1), all NULL until the admin saves something. Idempotent
-- seed below: INSERT OR IGNORE keeps the operator's values on every restart.
CREATE TABLE IF NOT EXISTS notify_settings (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	discord_webhook_url TEXT,
	telegram_bot_token TEXT,
	telegram_chat_id TEXT,
	updated_at INTEGER
);
INSERT OR IGNORE INTO notify_settings (id) VALUES (1);

-- Outbound notification preferences. A VPS policy acts as the default for
-- containers on that Docker Engine and an agent row is an explicit override.
-- Resource alerts default to disabled while offline/recovery notices stay on.
CREATE TABLE IF NOT EXISTS notification_policies (
	scope TEXT NOT NULL CHECK (scope IN ('vps','agent')),
	scope_id TEXT NOT NULL,
	notifications_enabled INTEGER NOT NULL CHECK (notifications_enabled IN (0,1)),
	resource_alerts_enabled INTEGER NOT NULL CHECK (resource_alerts_enabled IN (0,1)),
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (scope, scope_id)
);
`

// Store wraps the database handle and exposes typed queries.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path, applies the
// embedded migrations and returns a ready Store.
func Open(path string) (*Store, error) {
	// Pragmas are set in the DSN so every connection in the pool gets them.
	dsn := path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// Single-connection pool: SQLite serializes writes anyway and this
	// avoids SQLITE_BUSY contention between pooled connections.
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, stmt := range splitStatements(schema) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	// TASK-004 migration: persist uptime_sec on metrics. The CREATE TABLE
	// above never declares it, so the ALTER succeeds on both fresh and
	// previously migrated databases; the only expected failure is the
	// "duplicate column" of an already-migrated one, which is ignored.
	if _, err := db.ExecContext(ctx, `ALTER TABLE metrics ADD COLUMN uptime_sec INTEGER`); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("migrate uptime_sec: %w", err)
	}
	// TASK-009 migration: soft-dismiss timestamp on agents (nullable; NULL =
	// active agent). Same idempotence contract as uptime_sec above: an
	// already-migrated database fails with "duplicate column" and is ignored.
	if _, err := db.ExecContext(ctx, `ALTER TABLE agents ADD COLUMN dismissed_at INTEGER`); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("migrate agents.dismissed_at: %w", err)
	}
	// TASK-011 migrations (DEC-011): the token plaintext becomes PERSISTED
	// (the UI must always be able to show/copy the token — no more
	// one-time display), and tokens/agents gain a `tags` column: a JSON
	// array TEXT (e.g. '["lab","infra"]'). chatGPT-free format: plain
	// json.Marshal/Unmarshal of []string. Same duplicate-column idempotence.
	for _, m := range []struct{ name, stmt string }{
		{"tokens.token_plain", `ALTER TABLE tokens ADD COLUMN token_plain TEXT`},
		{"tokens.tags", `ALTER TABLE tokens ADD COLUMN tags TEXT`},
		{"agents.tags", `ALTER TABLE agents ADD COLUMN tags TEXT`},
	} {
		if _, err := db.ExecContext(ctx, m.stmt); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate %s: %w", m.name, err)
		}
	}
	// TASK-013 migrations (enrolamiento multi-agente): tokens gain the
	// `enrollment` flag (0 = normal 1:1 token, unchanged; 1 = fleet token —
	// N agents adopt on their first heartbeat) and agents gain `token_id`,
	// the explicit link used BOTH by enrollment agents AND (after the
	// retro-fill below) by 1:1 tokens. agents.token_hash keeps meaning
	// "created via the 1:1 channel"; enrollment agents carry NULL there so
	// many agents can share one enrollment token. Same duplicate-column
	// idempotence as above.
	for _, m := range []struct{ name, stmt string }{
		{"tokens.enrollment", `ALTER TABLE tokens ADD COLUMN enrollment INTEGER DEFAULT 0`},
		{"agents.token_id", `ALTER TABLE agents ADD COLUMN token_id INTEGER`},
	} {
		if _, err := db.ExecContext(ctx, m.stmt); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate %s: %w", m.name, err)
		}
	}
	// Retro-fill agents.token_id from the 1:1 links (tokens.agent_id) so
	// legacy/normal tokens report adopted_count correctly. Idempotent: rows
	// that already carry a token_id (enrollment adoptions) are untouched.
	if _, err := db.ExecContext(ctx,
		`UPDATE agents SET token_id = (SELECT t.id FROM tokens t WHERE t.agent_id = agents.id)
		 WHERE token_id IS NULL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate agents.token_id retro-fill: %w", err)
	}
	// Migración métrica de ALMACENAMIENTO (explorer v4 disk): snapshot df del
	// filesystem del container persistido en la fila del agent (NO en metrics —
	// es un snapshot por heartbeat, como uptime, sin rollup de history). NULL =
	// el agent nunca reportó disco. Patrón idempotente DEC-013 (uptime_sec):
	// un ALTER sobre una base ya migrada falla con "duplicate column" y se ignora.
	for _, m := range []struct{ name, stmt string }{
		{"agents.disk_free_mb", `ALTER TABLE agents ADD COLUMN disk_free_mb REAL`},
		{"agents.disk_total_mb", `ALTER TABLE agents ADD COLUMN disk_total_mb REAL`},
		{"agents.container_tags", `ALTER TABLE agents ADD COLUMN container_tags TEXT`},
		{"agents.monitoring_ignored", `ALTER TABLE agents ADD COLUMN monitoring_ignored INTEGER NOT NULL DEFAULT 0`},
		{"tokens.vps_id", `ALTER TABLE tokens ADD COLUMN vps_id TEXT`},
		{"tokens.vps_scoped", `ALTER TABLE tokens ADD COLUMN vps_scoped INTEGER NOT NULL DEFAULT 0`},
	} {
		if _, err := db.ExecContext(ctx, m.stmt); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate %s: %w", m.name, err)
		}
	}
	// A live VPS-scoped credential is bound to the first Docker Engine ID
	// reported by Stowaway. One active token per VPS prevents a credential
	// from silently spanning multiple hosts; revoked rows release the binding.
	if _, err := db.ExecContext(ctx,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_tokens_active_vps_id
		 ON tokens(vps_id) WHERE vps_id IS NOT NULL AND revoked_at IS NULL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate active VPS token index: %w", err)
	}
	return &Store{db: db}, nil
}

// splitStatements breaks a multi-statement script into individual,
// whitespace-trimmed statements (the driver handles one statement per Exec
// most reliably).
func splitStatements(script string) []string {
	parts := strings.Split(script, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// scannableRow covers *sql.Row and *sql.Rows for shared scan helpers.
type scannableRow interface {
	Scan(dest ...any) error
}
