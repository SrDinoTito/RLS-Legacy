package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestNotificationPoliciesInheritAndScopeByDockerEngine(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "policies.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	create := func(token, name, hostname, labels string) int64 {
		t.Helper()
		id, err := st.CreateAgent(ctx, token, name, hostname, "container-"+name, labels, 5, 1_700_000_000)
		if err != nil {
			t.Fatalf("create agent %s: %v", name, err)
		}
		return id
	}
	appA := create("token-a1", "api-a", "ubuntu", `{"rls_vps_id":"engine-A","rls_vps_name":"ubuntu"}`)
	workerA := create("token-a2", "worker-a", "ubuntu", `{"rls_vps_id":"engine-A","rls_vps_name":"ubuntu"}`)
	appB := create("token-b1", "api-b", "ubuntu", `{"rls_vps_id":"engine-B","rls_vps_name":"ubuntu"}`)

	// Safe defaults keep general outage notices on but suppress resource noise.
	if ok, err := st.ShouldNotifyAlert(ctx, appA, "offline"); err != nil || !ok {
		t.Fatalf("default offline policy: allowed=%v err=%v, want true", ok, err)
	}
	if ok, err := st.ShouldNotifyAlert(ctx, appA, "high_cpu"); err != nil || ok {
		t.Fatalf("default CPU policy: allowed=%v err=%v, want false", ok, err)
	}

	// VPS preferences use the Engine ID, so two VPSes with the same hostname
	// do not share mutes. New agents automatically inherit the VPS policy.
	if err := st.SetNotificationPolicy(ctx, NotificationScopeVPS, "engine-A", false, true, 123); err != nil {
		t.Fatalf("set VPS policy: %v", err)
	}
	for _, agentID := range []int64{appA, workerA} {
		if ok, err := st.ShouldNotifyAlert(ctx, agentID, "offline"); err != nil || ok {
			t.Fatalf("VPS mute should suppress offline for agent %d: allowed=%v err=%v", agentID, ok, err)
		}
		if ok, err := st.ShouldNotifyAlert(ctx, agentID, "high_ram"); err != nil || ok {
			t.Fatalf("VPS mute should suppress CPU/RAM too for agent %d: allowed=%v err=%v", agentID, ok, err)
		}
	}
	if ok, err := st.ShouldNotifyAlert(ctx, appB, "offline"); err != nil || !ok {
		t.Fatalf("same-hostname VPS leaked mute: allowed=%v err=%v, want true", ok, err)
	}

	// A container override cannot punch through a hard VPS mute. Under an
	// enabled VPS it can opt into consumption notices for just that agent.
	if err := st.SetNotificationPolicy(ctx, NotificationScopeAgent, "2", true, true, 124); err != nil {
		t.Fatalf("set agent override: %v", err)
	}
	if ok, err := st.ShouldNotifyAlert(ctx, workerA, "high_cpu"); err != nil || ok {
		t.Fatalf("VPS hard mute should block agent resource override: allowed=%v err=%v", ok, err)
	}
	if ok, err := st.ShouldNotifyAlert(ctx, workerA, "offline"); err != nil || ok {
		t.Fatalf("VPS hard mute should block agent outage override: allowed=%v err=%v", ok, err)
	}
	if err := st.SetNotificationPolicy(ctx, NotificationScopeAgent, "3", true, true, 125); err != nil {
		t.Fatalf("set override on enabled VPS: %v", err)
	}
	if ok, err := st.ShouldNotifyAlert(ctx, appB, "high_ram"); err != nil || !ok {
		t.Fatalf("agent resource override under enabled VPS: allowed=%v err=%v, want true", ok, err)
	}
	policy, err := st.GetNotificationPolicy(ctx, NotificationScopeAgent, "2")
	if err != nil || !policy.Customized || policy.Inherited || policy.ParentScopeID != "engine-A" || policy.NotificationsEnabled || policy.VPSNotificationsEnabled || !policy.ResourceAlertsEnabled {
		t.Fatalf("agent policy metadata: %+v err=%v", policy, err)
	}
	if err := st.DeleteNotificationPolicy(ctx, NotificationScopeAgent, "2"); err != nil {
		t.Fatalf("reset agent policy: %v", err)
	}
	policy, err = st.GetNotificationPolicy(ctx, NotificationScopeAgent, "2")
	if err != nil || !policy.Inherited || policy.NotificationsEnabled || !policy.ResourceAlertsEnabled {
		t.Fatalf("agent did not inherit VPS policy after reset: %+v err=%v", policy, err)
	}
}
