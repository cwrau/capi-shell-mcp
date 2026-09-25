package httpserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStartServesJSONRPCAtMcpAndRejectsWrongHost(t *testing.T) {
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.1"}, nil)

	srv, err := Start(mcpServer, 0, &recordingExecer{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	addr := srv.Addr()
	url := "http://" + addr + "/mcp"

	// Wrong Host header (simulated by hitting a URL whose host:port doesn't
	// match 127.0.0.1:<actual-port> — use a bogus port in the request URL's
	// authority so http.Client sends that mismatched Host header, but dial
	// the real listener via a custom transport).
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:1/mcp", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Host = "localhost"
	client := &http.Client{Transport: &fixedAddrTransport{addr: addr}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do (wrong host): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("status = %d, want %d for Host: localhost", resp.StatusCode, http.StatusMisdirectedRequest)
	}

	// Correct Host header reaches the MCP handler.
	req2, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Accept", "application/json, text/event-stream")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("Do (correct host): %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 for a correctly-addressed initialize request", resp2.StatusCode)
	}

	// Terminate the session cleanly before the deferred srv.Close runs.
	// Otherwise http.Server.Shutdown has to wait out this session's SSE
	// stream on its own, which is a race against however long the go-sdk
	// transport takes to notice the client is gone — flaky under load.
	if sessionID := resp2.Header.Get("Mcp-Session-Id"); sessionID != "" {
		delReq, err := http.NewRequest(http.MethodDelete, url, nil)
		if err != nil {
			t.Fatalf("NewRequest (DELETE): %v", err)
		}
		delReq.Header.Set("Mcp-Session-Id", sessionID)
		delResp, err := http.DefaultClient.Do(delReq)
		if err != nil {
			t.Fatalf("Do (DELETE session): %v", err)
		}
		delResp.Body.Close()
	}
}

func TestStartAndCloseReportStatusViaSystemdNotify(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "/run/systemd/notify")
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.1"}, nil)
	execer := &recordingExecer{}

	srv, err := Start(mcpServer, 0, execer)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if len(execer.calls) != 1 {
		t.Fatalf("calls after Start = %v, want exactly 1", execer.calls)
	}
	readyCall := execer.calls[0]
	if readyCall[0] != "systemd-notify" || readyCall[1] != "--ready" {
		t.Fatalf("Start call = %v, want systemd-notify --ready ...", readyCall)
	}
	wantStatus := "--status=listening on " + srv.Addr() + ", sessions=0"
	if readyCall[2] != wantStatus {
		t.Errorf("Start status = %q, want %q", readyCall[2], wantStatus)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if len(execer.calls) != 2 {
		t.Fatalf("calls after Close = %v, want exactly 2", execer.calls)
	}
	stopCall := execer.calls[1]
	want := []string{"systemd-notify", "--stopping", "--status=shutting down"}
	if len(stopCall) != len(want) {
		t.Fatalf("Close call = %v, want %v", stopCall, want)
	}
	for i, arg := range want {
		if stopCall[i] != arg {
			t.Errorf("Close call[%d] = %q, want %q", i, stopCall[i], arg)
		}
	}
}

// fixedAddrTransport redials every request to addr regardless of the
// request URL's own host:port, so a deliberately-wrong request URL can
// still reach the real ephemeral-port listener while sending whatever
// Host header the test set explicitly.
type fixedAddrTransport struct{ addr string }

func (t *fixedAddrTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Host = t.addr
	return http.DefaultTransport.RoundTrip(clone)
}
