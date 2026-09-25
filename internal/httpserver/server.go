// Package httpserver runs the MCP server over the streamable-HTTP
// transport, bound to 127.0.0.1 only, with a strict Host-header check on
// top of the go-sdk's own DNS-rebinding protection.
package httpserver

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/cwrau/capi-shell-mcp/internal/shell"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server is a running MCP-over-HTTP daemon.
type Server struct {
	httpServer *http.Server
	listener   net.Listener
	execer     shell.Execer
}

// Start binds 127.0.0.1:port (port 0 picks an ephemeral port, for tests),
// serves mcpServer's tools at /mcp, and signals readiness to systemd if
// NOTIFY_SOCKET is set. The same *mcp.Server instance is used for every
// session — the go-sdk permits this, and it keeps tool registration and
// caches at process scope, matching this daemon's shared-cache design.
func Start(mcpServer *mcp.Server, port int, execer shell.Execer) (*Server, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("httpserver: listening on 127.0.0.1:%d: %w", port, err)
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", hostCheckMiddleware(actualPort, handler))

	httpSrv := &http.Server{Handler: mux}
	go func() { _ = httpSrv.Serve(ln) }() // http.ErrServerClosed after Shutdown is expected

	addr := ln.Addr().String()
	notifySystemd(execer, "--ready", fmt.Sprintf("--status=listening on %s, sessions=%d", addr, sessionCount(mcpServer)))

	return &Server{httpServer: httpSrv, listener: ln, execer: execer}, nil
}

// sessionCount returns the number of currently connected MCP sessions —
// used to report a live count in the systemd STATUS field. Works
// regardless of which initialize handshake a client negotiated, unlike
// hooking ServerOptions.InitializedHandler (which the newer server/discover
// handshake never calls).
func sessionCount(mcpServer *mcp.Server) int {
	n := 0
	for range mcpServer.Sessions() {
		n++
	}
	return n
}

// Addr returns the bound "host:port" address (useful when Start was given
// port 0).
func (s *Server) Addr() string {
	return s.listener.Addr().String()
}

// Close gracefully shuts down the HTTP server and notifies systemd.
func (s *Server) Close(ctx context.Context) error {
	err := s.httpServer.Shutdown(ctx)
	notifySystemd(s.execer, "--stopping", "--status=shutting down")
	return err
}
