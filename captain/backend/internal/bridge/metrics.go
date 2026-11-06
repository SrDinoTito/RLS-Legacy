package bridge

import (
	"math"
	"strings"
	"time"
)

// Metrics matches the existing RLS Explorer heartbeat contract.
type Metrics struct {
	CPUPct     *float64 `json:"cpu_pct,omitempty"`
	RamUsedMB  *float64 `json:"ram_used_mb,omitempty"`
	RamLimitMB *float64 `json:"ram_limit_mb,omitempty"`
	RxMB       *float64 `json:"rx_mb,omitempty"`
	TxMB       *float64 `json:"tx_mb,omitempty"`
	UptimeSec  *float64 `json:"uptime_sec,omitempty"`
}

// MetricsFromDocker converts one point-in-time Docker API stats sample to the
// same units as explorer.sh. Disk is intentionally omitted: Engine stats do
// not report a comparable per-container `df /` snapshot.
func MetricsFromDocker(stats DockerStats, info ContainerInspect, now time.Time) Metrics {
	var out Metrics

	cpuDelta := delta(stats.CPUStats.CPUUsage.TotalUsage, stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := delta(stats.CPUStats.SystemCPUUsage, stats.PreCPUStats.SystemCPUUsage)
	if systemDelta > 0 && cpuDelta > 0 {
		cpus := stats.CPUStats.OnlineCPUs
		if cpus == 0 {
			cpus = uint32(len(stats.CPUStats.CPUUsage.PerCPU))
		}
		if cpus == 0 {
			cpus = 1
		}
		out.CPUPct = floatPtr((float64(cpuDelta) / float64(systemDelta)) * float64(cpus) * 100)
	}

	used := stats.Memory.Usage
	cache := stats.Memory.Stats["total_inactive_file"]
	if stats.Memory.Stats["inactive_file"] > cache {
		cache = stats.Memory.Stats["inactive_file"]
	}
	if cache < used {
		used -= cache
	}
	out.RamUsedMB = floatPtr(float64(used) / 1048576)
	if info.HostConfig.Memory > 0 {
		out.RamLimitMB = floatPtr(float64(info.HostConfig.Memory) / 1048576)
	}

	var rx, tx uint64
	for _, network := range stats.Networks {
		rx += network.RxBytes
		tx += network.TxBytes
	}
	if len(stats.Networks) > 0 {
		out.RxMB = floatPtr(float64(rx) / 1048576)
		out.TxMB = floatPtr(float64(tx) / 1048576)
	}

	if started, err := time.Parse(time.RFC3339Nano, info.State.StartedAt); err == nil && !started.IsZero() {
		seconds := now.Sub(started).Seconds()
		if seconds < 0 {
			seconds = 0
		}
		out.UptimeSec = floatPtr(seconds)
	}
	return out
}

func delta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func floatPtr(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

// AgentName provides a stable, cross-host identity for an enrollment token.
// For Compose/Swarm workloads it favors project/service/replica labels; the
// Docker name and short ID are the fallback for unmanaged containers.
func AgentName(host string, summary ContainerSummary, info ContainerInspect) string {
	labels := make(map[string]string, len(summary.Labels)+len(info.Config.Labels))
	for key, value := range summary.Labels {
		labels[key] = value
	}
	for key, value := range info.Config.Labels {
		labels[key] = value
	}
	// Swarm's task slot is not present in every Engine API response, even
	// though task.name is (for example, "dokploy.1.<task-id>"). Keep the
	// stable replica part before reducing the labels to our allowlist.
	swarmTaskName := firstLabel(labels, "com.docker.swarm.task.name")
	labels = ContainerLabels(labels)

	project := firstLabel(labels, "dokploy_project", "com.docker.compose.project", "com.docker.stack.namespace")
	environment := labels["dokploy_environment"]
	application := labels["dokploy_application"]
	service := firstLabel(labels, "dokploy_service", "com.docker.compose.service", "com.docker.swarm.service.name")
	replica := firstLabel(labels, "com.docker.compose.container-number", "com.docker.swarm.task.slot")
	if replica == "" {
		replica = swarmTaskReplica(swarmTaskName)
	}
	parts := []string{host}
	for _, part := range []string{labels["dokploy_organization"], project, environment, application, service, replica} {
		if strings.TrimSpace(part) != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 1 {
		name := strings.TrimPrefix(info.Name, "/")
		if name == "" && len(summary.Names) > 0 {
			name = strings.TrimPrefix(summary.Names[0], "/")
		}
		if name != "" {
			parts = append(parts, name)
		}
	}
	// If the platform supplies no replica slot, a short ID prevents two
	// containers from claiming the same enrollment identity.
	if replica == "" && summary.ID != "" {
		id := summary.ID
		if len(id) > 8 {
			id = id[:8]
		}
		parts = append(parts, id)
	}

	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = sanitizeNamePart(part)
		if part != "" {
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, "_")
}

func firstLabel(labels map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(labels[key]); value != "" {
			return value
		}
	}
	return ""
}

// swarmTaskReplica extracts the stable numeric slot from Swarm task names of
// the form "service.slot.task-id". Parse from the right so service names may
// themselves contain dots. The task ID is intentionally discarded because
// Swarm replaces it when a task is recreated after a host restart.
func swarmTaskReplica(taskName string) string {
	lastDot := strings.LastIndex(taskName, ".")
	if lastDot <= 0 || lastDot == len(taskName)-1 {
		return ""
	}
	prefix := taskName[:lastDot]
	previousDot := strings.LastIndex(prefix, ".")
	if previousDot < 0 || previousDot == len(prefix)-1 {
		return ""
	}
	slot := prefix[previousDot+1:]
	for _, r := range slot {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return slot
}

func sanitizeNamePart(value string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.TrimSpace(value) {
		allowed := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if allowed {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	return strings.Trim(b.String(), "-_.")
}
