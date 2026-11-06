// Command stowaway monitors local Docker containers and reports their
// metrics to Captain using an existing enrollment token.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/rls-legacy/captain/backend/internal/bridge"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		_, _ = os.Stdout.WriteString("RLS Stowaway " + version + "\n")
		return
	}

	cfg, err := bridge.ConfigFromEnv()
	if err != nil {
		slog.Error("configuración inválida", "error", err)
		os.Exit(2)
	}
	collector, err := bridge.NewCollector(cfg, slog.Default())
	if err != nil {
		slog.Error("no se pudo inicializar el recolector", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	collector.Run(ctx)
}
