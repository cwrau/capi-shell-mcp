// Command capi-shell-mcp runs the MCP daemon: it loads config, wires the
// CAPI cluster caches and sshuttle proxy manager, mounts the 7 bespoke
// tools, and serves them over HTTP at 127.0.0.1.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
	"github.com/cwrau/capi-shell-mcp/internal/config"
	"github.com/cwrau/capi-shell-mcp/internal/embed"
	"github.com/cwrau/capi-shell-mcp/internal/httpserver"
	"github.com/cwrau/capi-shell-mcp/internal/proxy"
	"github.com/cwrau/capi-shell-mcp/internal/server"
	"github.com/cwrau/capi-shell-mcp/internal/shell"
)

const defaultPort = 4737

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	store := capi.NewStore(
		time.Duration(cfg.Cache.ClusterListTTLSeconds)*time.Second,
		time.Duration(cfg.Cache.KubeconfigTTLSeconds)*time.Second,
	)

	execer := shell.OSExecer{}
	proxyMgr := proxy.New(proxy.RealClock{}, execer, proxy.DefaultSpawnProcess, proxy.IsUnderSystemd)
	// Only directly-spawned (non-systemd) tunnels need this: systemd-managed
	// ones are meant to outlive the daemon (see proxy.Manager's docs).
	defer proxyMgr.KillAllProcessProxies()

	toolServer := server.NewToolServer(cfg, store, proxyMgr, execer, server.RealClientsForContext)
	mcpServer := server.BuildServer(toolServer)

	if err := embed.MountTools(context.Background(), mcpServer, toolServer); err != nil {
		return fmt.Errorf("mounting kubernetes-mcp-server tools: %w", err)
	}

	port := defaultPort
	if p := os.Getenv("CAPI_SHELL_MCP_PORT"); p != "" {
		parsed, err := strconv.Atoi(p)
		if err != nil {
			return fmt.Errorf("invalid CAPI_SHELL_MCP_PORT %q: %w", p, err)
		}
		port = parsed
	}

	httpSrv, err := httpserver.Start(mcpServer, port, execer)
	if err != nil {
		return fmt.Errorf("starting HTTP server: %w", err)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Close(ctx)
}
