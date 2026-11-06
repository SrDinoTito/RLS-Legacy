package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const collectorWorkers = 6

type heartbeat struct {
	Hostname    string            `json:"hostname"`
	TS          int64             `json:"ts"`
	Interval    int               `json:"interval"`
	Name        string            `json:"name"`
	ContainerID string            `json:"container_id"`
	Labels      map[string]string `json:"labels"`
	Metrics     Metrics           `json:"metrics"`
}

// Collector periodically reconciles the local Docker daemon and reports one
// heartbeat per running container. It does not expose any inbound listener.
type Collector struct {
	config    Config
	docker    *DockerClient
	remote    *http.Client
	heartbeat string
	logger    *slog.Logger
}

func NewCollector(config Config, logger *slog.Logger) (*Collector, error) {
	if logger == nil {
		logger = slog.Default()
	}
	endpoint, err := url.JoinPath(config.DashboardURL, "api/v1/agents/heartbeat")
	if err != nil {
		return nil, fmt.Errorf("build heartbeat URL: %w", err)
	}
	return &Collector{
		config:    config,
		docker:    NewDockerClient(config.DockerSocket),
		remote:    &http.Client{Timeout: 8 * time.Second},
		heartbeat: endpoint,
		logger:    logger,
	}, nil
}

func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.config.Interval)
	defer ticker.Stop()
	c.logger.Info("RLS Stowaway iniciado", "host", c.config.Hostname, "interval_sec", int(c.config.Interval.Seconds()))
	for {
		c.collect(ctx)
		select {
		case <-ctx.Done():
			c.logger.Info("RLS Stowaway detenido")
			return
		case <-ticker.C:
		}
	}
}

func (c *Collector) collect(ctx context.Context) {
	cycleCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	containers, err := c.docker.Containers(cycleCtx)
	if err != nil {
		c.logger.Warn("no se pudo consultar Docker Engine; se reintentará", "error", err)
		return
	}
	identityHost := c.config.Hostname
	vpsID := c.config.Hostname
	if engineID, err := c.docker.EngineID(cycleCtx); err == nil {
		vpsID = engineID
		if len(engineID) > 8 {
			engineID = engineID[:8]
		}
		identityHost += "-" + engineID
	} else {
		c.logger.Warn("no se pudo obtener identidad de Docker; se usará hostname", "error", err)
	}
	if len(containers) == 0 {
		return
	}
	workers := collectorWorkers
	if len(containers) < workers {
		workers = len(containers)
	}
	jobs := make(chan ContainerSummary)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for container := range jobs {
				if cycleCtx.Err() != nil {
					return
				}
				c.reportContainer(cycleCtx, identityHost, vpsID, container)
			}
		}()
	}

enqueue:
	for _, container := range containers {
		if container.State != "running" || container.ID == "" {
			continue
		}
		select {
		case jobs <- container:
		case <-cycleCtx.Done():
			break enqueue
		}
	}
	close(jobs)
	wg.Wait()
}

func (c *Collector) reportContainer(ctx context.Context, identityHost, vpsID string, summary ContainerSummary) {
	info, err := c.docker.Inspect(ctx, summary.ID)
	if err != nil {
		c.logger.Warn("no se pudo inspeccionar contenedor", "container_id", shortID(summary.ID), "error", err)
		return
	}
	if !info.State.Running {
		return
	}
	stats, err := c.docker.Stats(ctx, summary.ID)
	if err != nil {
		c.logger.Warn("no se pudieron leer métricas del contenedor", "container_id", shortID(summary.ID), "error", err)
		return
	}
	now := time.Now()
	name := AgentName(identityHost, summary, info)
	labels := ContainerLabels(summary.Labels)
	for key, value := range ContainerLabels(info.Config.Labels) {
		labels[key] = value
	}
	labels["rls_collector"] = "stowaway"
	// The Engine ID is stable across agent restarts and unique even when two
	// VPSes share a generic hostname such as "ubuntu". Keep a friendly host
	// label too so the UI can distinguish duplicate names without exposing
	// any container environment or secrets.
	addVPSIdentityLabels(labels, vpsID, c.config.Hostname)

	beat := heartbeat{
		Hostname:    c.config.Hostname,
		TS:          now.UnixMilli(),
		Interval:    int(c.config.Interval.Seconds()),
		Name:        name,
		ContainerID: summary.ID,
		Labels:      labels,
		Metrics:     MetricsFromDocker(stats, info, now),
	}
	if err := c.sendHeartbeat(ctx, beat); err != nil {
		c.logger.Warn("falló el heartbeat", "agent", name, "error", err)
	}
}

func addVPSIdentityLabels(labels map[string]string, vpsID, hostname string) {
	labels["rls_vps_id"] = vpsID
	labels["rls_vps_name"] = hostname
}

func (c *Collector) sendHeartbeat(ctx context.Context, beat heartbeat) error {
	body, err := json.Marshal(beat)
	if err != nil {
		return fmt.Errorf("encode heartbeat: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.heartbeat, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create heartbeat request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.config.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.remote.Do(req)
	if err != nil {
		return fmt.Errorf("send heartbeat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("dashboard returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func shortID(id string) string {
	return strings.TrimSpace(id[:min(len(id), 12)])
}
