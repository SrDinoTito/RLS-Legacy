package bridge

import (
	"math"
	"testing"
	"time"
)

func TestMetricsFromDockerUsesComparableUnits(t *testing.T) {
	started := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	now := started.Add(90 * time.Second)
	stats := DockerStats{
		CPUStats: cpuStats{
			CPUUsage:       cpuUsage{TotalUsage: 300, PerCPU: []uint64{1, 2}},
			SystemCPUUsage: 300,
			OnlineCPUs:     2,
		},
		PreCPUStats: cpuStats{
			CPUUsage:       cpuUsage{TotalUsage: 100},
			SystemCPUUsage: 200,
		},
		Memory: memoryStats{
			Usage: 100 * 1048576,
			Stats: map[string]uint64{"total_inactive_file": 20 * 1048576},
		},
		Networks: map[string]networkStats{
			"eth0": {RxBytes: 10 * 1048576, TxBytes: 5 * 1048576},
			"eth1": {RxBytes: 2 * 1048576, TxBytes: 1 * 1048576},
		},
	}
	info := ContainerInspect{HostConfig: struct {
		Memory int64 `json:"Memory"`
	}{Memory: 256 * 1048576}}
	info.State.StartedAt = started.Format(time.RFC3339Nano)
	metrics := MetricsFromDocker(stats, info, now)

	assertFloat(t, metrics.CPUPct, 400)
	assertFloat(t, metrics.RamUsedMB, 80)
	assertFloat(t, metrics.RamLimitMB, 256)
	assertFloat(t, metrics.RxMB, 12)
	assertFloat(t, metrics.TxMB, 6)
	assertFloat(t, metrics.UptimeSec, 90)
}

func TestMetricsFromDockerOmitsUnlimitedRamAndUnavailableDisk(t *testing.T) {
	info := ContainerInspect{}
	info.State.StartedAt = time.Now().Add(time.Minute).Format(time.RFC3339Nano)
	metrics := MetricsFromDocker(DockerStats{Memory: memoryStats{Usage: 32}}, info, time.Now())
	if metrics.RamLimitMB != nil || metrics.UptimeSec == nil || *metrics.UptimeSec != 0 {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
}

func TestAgentNameUsesComposeIdentityAndSeparatesReplicas(t *testing.T) {
	base := ContainerSummary{ID: "0123456789abcdef", Labels: map[string]string{
		"com.docker.compose.project": "web-prod",
		"com.docker.compose.service": "api",
	}}
	info := ContainerInspect{}
	info.Config.Labels = base.Labels
	first := AgentName("host-a", base, info)
	base.Labels["com.docker.compose.container-number"] = "1"
	info.Config.Labels = base.Labels
	first = AgentName("host-a", base, info)
	base.Labels["com.docker.compose.container-number"] = "2"
	info.Config.Labels = base.Labels
	second := AgentName("host-a", base, info)
	if first == second || first != "host-a_web-prod_api_1" || second != "host-a_web-prod_api_2" {
		t.Fatalf("replica names not stable/unique: %q, %q", first, second)
	}
}

func TestAgentNameUsesStableSwarmTaskSlotWhenTaskSlotLabelIsMissing(t *testing.T) {
	first := ContainerSummary{ID: "old-task-container-id", Labels: map[string]string{
		"com.docker.swarm.service.name": "dokploy",
		"com.docker.swarm.task.name":    "dokploy.1.oldtaskid",
	}}
	firstInfo := ContainerInspect{}
	firstInfo.Config.Labels = first.Labels
	firstName := AgentName("host-a", first, firstInfo)

	second := ContainerSummary{ID: "new-task-container-id", Labels: map[string]string{
		"com.docker.swarm.service.name": "dokploy",
		"com.docker.swarm.task.name":    "dokploy.1.newtaskid",
	}}
	secondInfo := ContainerInspect{}
	secondInfo.Config.Labels = second.Labels
	secondName := AgentName("host-a", second, secondInfo)

	if firstName != "host-a_dokploy_1" || secondName != firstName {
		t.Fatalf("Swarm task replacement changed agent identity: first=%q second=%q", firstName, secondName)
	}
}

func TestSwarmTaskReplicaParsesFromRightAndRejectsMalformedNames(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "dokploy.1.taskid", want: "1"},
		{name: "team.api.gateway.12.taskid", want: "12"},
		{name: "service.not-a-slot.taskid", want: ""},
		{name: "service.1", want: ""},
		{name: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := swarmTaskReplica(tc.name); got != tc.want {
				t.Fatalf("swarmTaskReplica(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestContainerLabelsAllowlist(t *testing.T) {
	got := ContainerLabels(map[string]string{
		"dokploy_project":            "project",
		"com.docker.compose.service": "api",
		"sensitive.example.com":      "do-not-forward",
	})
	if len(got) != 2 || got["sensitive.example.com"] != "" {
		t.Fatalf("unexpected labels: %#v", got)
	}
}

func assertFloat(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > 0.001 {
		t.Fatalf("got %v, want %v", got, want)
	}
}
