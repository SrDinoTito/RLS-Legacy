package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenMergesExistingDuplicateVPSIdentities(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "duplicate-vps-agents.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO tokens (id, token_hash, name, created_at, vps_id, vps_scoped, enrollment)
		 VALUES (1, 'hash', 'test-vps', 1, 'engine-test', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []struct {
		id          int64
		containerID string
		status      string
		lastSeen    int64
	}{
		{id: 10, containerID: "old-container-id", status: "missing", lastSeen: 100},
		{id: 11, containerID: "new-container-id", status: "online", lastSeen: 200},
	} {
		if _, err := st.db.ExecContext(ctx,
			`INSERT INTO agents (id, token_hash, token_id, name, hostname, container_id, interval, status, last_seen, created_at)
			 VALUES (?, NULL, 1, 'engine_project_api_1', 'test-vps', ?, 10, ?, ?, ?)`,
			agent.id, agent.containerID, agent.status, agent.lastSeen, agent.lastSeen); err != nil {
			t.Fatal(err)
		}
	}
	for _, sample := range []struct {
		agentID int64
		ts      int64
	}{
		{agentID: 10, ts: 100},
		{agentID: 11, ts: 200},
	} {
		if _, err := st.db.ExecContext(ctx,
			`INSERT INTO metrics (agent_id, ts, cpu_pct, ram_used_mb) VALUES (?, ?, 5, 8)`, sample.agentID, sample.ts); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO alerts (id, agent_id, type, fired_at) VALUES (1, 10, 'container_missing', 110)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO notification_policies (scope, scope_id, notifications_enabled, resource_alerts_enabled, updated_at)
		 VALUES ('agent', '10', 0, 1, 150)`); err != nil {
		t.Fatal(err)
	}
	// Remove the new-version index to model a legacy DB containing duplicate
	// identities before the upgrade's backfill/merge step runs.
	if _, err := st.db.ExecContext(ctx, `DROP INDEX idx_agents_token_identity`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening simulates upgrading a database created before identity_key.
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	agents, err := st.ListAgentsDetailed(ctx, true)
	if err != nil || len(agents) != 1 {
		t.Fatalf("duplicate rows were not merged: rows=%d err=%v", len(agents), err)
	}
	if agents[0].ID != 11 || agents[0].ContainerID.String != "new-container-id" || agents[0].Status != "online" {
		t.Fatalf("migration did not retain the current instance: %+v", agents[0])
	}
	canonical, err := st.AgentByID(ctx, 11)
	if err != nil || !canonical.IdentityKey.Valid || canonical.IdentityKey.String != "engine_project_api_1" {
		t.Fatalf("logical identity was not backfilled: %+v err=%v", canonical, err)
	}
	history, err := st.HistoryRollup(ctx, 11, 90, 210, 1)
	if err != nil || len(history) != 2 {
		t.Fatalf("historical metric samples were not preserved: buckets=%d err=%v", len(history), err)
	}
	alerts, err := st.ListAlerts(ctx, 11)
	if err != nil || len(alerts) != 1 || alerts[0].Type != "container_missing" || !alerts[0].ResolvedAt.Valid {
		t.Fatalf("old lifecycle alert was not retained and resolved: %+v err=%v", alerts, err)
	}
	var policyCount int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notification_policies WHERE scope = 'agent' AND scope_id = '11'`).Scan(&policyCount); err != nil || policyCount != 1 {
		t.Fatalf("notification settings were not transferred: count=%d err=%v", policyCount, err)
	}
	var duplicateCount int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agents WHERE token_id = 1 AND identity_key = 'engine_project_api_1'`).Scan(&duplicateCount); err != nil || duplicateCount != 1 {
		t.Fatalf("unique logical identity count=%d err=%v", duplicateCount, err)
	}
}
