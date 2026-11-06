package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const dockerRequestTimeout = 8 * time.Second

var dockerAPIVersionPattern = regexp.MustCompile(`^\d+\.\d+$`)

// DockerClient uses the local Engine HTTP API over a Unix socket. Call sites
// intentionally issue read-only requests only; the socket itself remains a
// privileged capability and must be treated as such.
type DockerClient struct {
	socket string
	http   *http.Client
	mu     sync.Mutex
	api    string
	engine string
}

func NewDockerClient(socket string) *DockerClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: false,
	}
	return &DockerClient{
		socket: socket,
		http:   &http.Client{Transport: transport, Timeout: dockerRequestTimeout},
	}
}

type dockerVersion struct {
	APIVersion string `json:"ApiVersion"`
}

type dockerInfo struct {
	ID string `json:"ID"`
}

func (d *DockerClient) apiVersion(ctx context.Context) (string, error) {
	d.mu.Lock()
	if d.api != "" {
		v := d.api
		d.mu.Unlock()
		return v, nil
	}
	d.mu.Unlock()

	var version dockerVersion
	if err := d.getJSON(ctx, "/version", &version); err != nil {
		return "", err
	}
	if !dockerAPIVersionPattern.MatchString(version.APIVersion) {
		return "", fmt.Errorf("docker daemon returned an invalid API version")
	}
	d.mu.Lock()
	if d.api == "" {
		d.api = version.APIVersion
	}
	v := d.api
	d.mu.Unlock()
	return v, nil
}

type ContainerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
}

type ContainerInspect struct {
	Name   string `json:"Name"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		Memory int64 `json:"Memory"`
	} `json:"HostConfig"`
	State struct {
		Running   bool   `json:"Running"`
		StartedAt string `json:"StartedAt"`
	} `json:"State"`
}

type cpuUsage struct {
	TotalUsage uint64   `json:"total_usage"`
	PerCPU     []uint64 `json:"percpu_usage"`
}

type cpuStats struct {
	CPUUsage       cpuUsage `json:"cpu_usage"`
	SystemCPUUsage uint64   `json:"system_cpu_usage"`
	OnlineCPUs     uint32   `json:"online_cpus"`
}

type memoryStats struct {
	Usage uint64            `json:"usage"`
	Limit uint64            `json:"limit"`
	Stats map[string]uint64 `json:"stats"`
}

type networkStats struct {
	RxBytes uint64 `json:"rx_bytes"`
	TxBytes uint64 `json:"tx_bytes"`
}

type DockerStats struct {
	CPUStats    cpuStats                `json:"cpu_stats"`
	PreCPUStats cpuStats                `json:"precpu_stats"`
	Memory      memoryStats             `json:"memory_stats"`
	Networks    map[string]networkStats `json:"networks"`
}

func (d *DockerClient) Containers(ctx context.Context) ([]ContainerSummary, error) {
	version, err := d.apiVersion(ctx)
	if err != nil {
		return nil, err
	}
	var containers []ContainerSummary
	if err := d.getJSON(ctx, "/v"+version+"/containers/json?all=0", &containers); err != nil {
		return nil, err
	}
	return containers, nil
}

// EngineID returns the stable ID of the Docker daemon, used to avoid
// enrollment-name collisions when separate hosts share the same hostname.
func (d *DockerClient) EngineID(ctx context.Context) (string, error) {
	d.mu.Lock()
	if d.engine != "" {
		id := d.engine
		d.mu.Unlock()
		return id, nil
	}
	d.mu.Unlock()
	version, err := d.apiVersion(ctx)
	if err != nil {
		return "", err
	}
	var info dockerInfo
	if err := d.getJSON(ctx, "/v"+version+"/info", &info); err != nil {
		return "", err
	}
	if strings.TrimSpace(info.ID) == "" {
		return "", fmt.Errorf("docker daemon returned an empty engine ID")
	}
	d.mu.Lock()
	if d.engine == "" {
		d.engine = info.ID
	}
	id := d.engine
	d.mu.Unlock()
	return id, nil
}

func (d *DockerClient) Inspect(ctx context.Context, id string) (ContainerInspect, error) {
	version, err := d.apiVersion(ctx)
	if err != nil {
		return ContainerInspect{}, err
	}
	var info ContainerInspect
	path := "/v" + version + "/containers/" + url.PathEscape(id) + "/json"
	if err := d.getJSON(ctx, path, &info); err != nil {
		return ContainerInspect{}, err
	}
	return info, nil
}

func (d *DockerClient) Stats(ctx context.Context, id string) (DockerStats, error) {
	version, err := d.apiVersion(ctx)
	if err != nil {
		return DockerStats{}, err
	}
	var stats DockerStats
	path := "/v" + version + "/containers/" + url.PathEscape(id) + "/stats?stream=false"
	if err := d.getJSON(ctx, path, &stats); err != nil {
		return DockerStats{}, err
	}
	return stats, nil
}

func (d *DockerClient) getJSON(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return fmt.Errorf("create docker request: %w", err)
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("connect to docker socket %s: %w", d.socket, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("docker API returned HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(dst); err != nil {
		return fmt.Errorf("decode docker API response: %w", err)
	}
	return nil
}

// ContainerLabels keeps only metadata known to be useful for grouping. Docker
// Config.Env and arbitrary labels are deliberately never requested or sent.
func ContainerLabels(labels map[string]string) map[string]string {
	allowed := []string{
		"dokploy_organization", "dokploy_project", "dokploy_environment",
		"dokploy_application", "dokploy_service",
		"com.docker.compose.project", "com.docker.compose.service",
		"com.docker.compose.container-number", "com.docker.stack.namespace",
		"com.docker.swarm.service.name", "com.docker.swarm.task.slot",
	}
	result := make(map[string]string)
	for _, key := range allowed {
		if value := strings.TrimSpace(labels[key]); value != "" {
			result[key] = value
		}
	}
	return result
}
