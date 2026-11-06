package bridge

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestDockerClientNegotiatesVersionAndReadsOnlyContainerState(t *testing.T) {
	// Windows AF_UNIX paths have a short maximum length. t.TempDir() includes
	// the long test name, so place this uniquely named socket directly in the
	// system temp directory instead.
	socket := filepath.Join(os.TempDir(), "rls-docker-"+fmt.Sprint(os.Getpid())+".sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(socket) })
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"ApiVersion":"1.47"}`))
		case "/v1.47/info":
			_, _ = w.Write([]byte(`{"ID":"engine-xyz987"}`))
		case "/v1.47/containers/json":
			if r.URL.Query().Get("all") != "0" {
				t.Errorf("expected running containers only, query=%s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"Id":"abc123","Names":["/api"],"State":"running","Labels":{"com.docker.compose.service":"api"}}]`))
		case "/v1.47/containers/abc123/json":
			_, _ = w.Write([]byte(`{"Name":"/api","Config":{"Labels":{"com.docker.compose.project":"demo"}},"HostConfig":{"Memory":67108864},"State":{"Running":true,"StartedAt":"2026-11-02T10:00:00Z"}}`))
		case "/v1.47/containers/abc123/stats":
			if r.URL.Query().Get("stream") != "false" {
				t.Errorf("expected one-shot stats, query=%s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"cpu_stats":{"cpu_usage":{"total_usage":20,"percpu_usage":[1]},"system_cpu_usage":200,"online_cpus":1},"precpu_stats":{"cpu_usage":{"total_usage":10},"system_cpu_usage":100},"memory_stats":{"usage":1024,"limit":67108864,"stats":{}},"networks":{"eth0":{"rx_bytes":10,"tx_bytes":20}}}`))
		default:
			t.Errorf("unexpected Docker API request: %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	client := NewDockerClient(socket)
	engineID, err := client.EngineID(context.Background())
	if err != nil || engineID != "engine-xyz987" {
		t.Fatalf("engine ID=%q err=%v", engineID, err)
	}
	containers, err := client.Containers(context.Background())
	if err != nil || len(containers) != 1 || containers[0].ID != "abc123" {
		t.Fatalf("containers=%+v err=%v", containers, err)
	}
	info, err := client.Inspect(context.Background(), "abc123")
	if err != nil || !info.State.Running || info.HostConfig.Memory != 67108864 {
		t.Fatalf("inspect=%+v err=%v", info, err)
	}
	stats, err := client.Stats(context.Background(), "abc123")
	if err != nil || stats.Networks["eth0"].RxBytes != 10 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}
