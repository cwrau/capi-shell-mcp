package httpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cwrau/capi-shell-mcp/internal/shell"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type recordingExecer struct {
	calls [][]string
}

func (e *recordingExecer) ExecFile(_ context.Context, file string, args []string, _ shell.Options) (shell.Output, error) {
	e.calls = append(e.calls, append([]string{file}, args...))
	return shell.Output{}, nil
}

func TestNotifySystemdNoopWhenNotifySocketUnset(t *testing.T) {
	// The ambient environment may be a systemd unit that already has this
	// set; the code treats empty as unset, so force that.
	t.Setenv("NOTIFY_SOCKET", "")
	execer := &recordingExecer{}
	notifySystemd(execer, "--ready")
	if len(execer.calls) != 0 {
		t.Errorf("calls = %v, want none when NOTIFY_SOCKET is unset", execer.calls)
	}
}

func TestNotifySystemdCallsSystemdNotifyWhenNotifySocketSet(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "/run/systemd/notify")
	execer := &recordingExecer{}
	notifySystemd(execer, "--ready")
	if len(execer.calls) != 1 || execer.calls[0][0] != "systemd-notify" || execer.calls[0][1] != "--ready" {
		t.Errorf("calls = %v, want one call to systemd-notify --ready", execer.calls)
	}
}

func TestNotifySystemdPassesEveryArgSeparately(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "/run/systemd/notify")
	execer := &recordingExecer{}
	notifySystemd(execer, "--ready", "--status=listening on 127.0.0.1:4737, sessions=0")
	want := []string{"systemd-notify", "--ready", "--status=listening on 127.0.0.1:4737, sessions=0"}
	if len(execer.calls) != 1 || len(execer.calls[0]) != len(want) {
		t.Fatalf("calls = %v, want one call with %v", execer.calls, want)
	}
	for i, arg := range want {
		if execer.calls[0][i] != arg {
			t.Errorf("calls[0][%d] = %q, want %q", i, execer.calls[0][i], arg)
		}
	}
}

func TestReloadBracketsApplyWithReloadingAndReady(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "/run/systemd/notify")
	for name, applyErr := range map[string]error{"ok": nil, "failed": errors.New("bad yaml")} {
		t.Run(name, func(t *testing.T) {
			execer := &recordingExecer{}
			mcpServer := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.1"}, nil)
			srv, err := Start(mcpServer, 0, execer)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer srv.Close(context.Background())
			execer.calls = nil

			applied := false
			got := srv.Reload(mcpServer, func() error {
				applied = len(execer.calls) == 1 // RELOADING went out before apply ran
				return applyErr
			})

			if got != applyErr {
				t.Errorf("Reload returned %v, want %v", got, applyErr)
			}
			if !applied {
				t.Error("apply did not run after the --reloading notification")
			}
			if len(execer.calls) != 2 || execer.calls[0][1] != "--reloading" || execer.calls[1][1] != "--ready" {
				t.Fatalf("calls = %v, want --reloading then --ready", execer.calls)
			}
			status := execer.calls[1][2]
			if failed := strings.Contains(status, "bad yaml"); failed != (applyErr != nil) {
				t.Errorf("status %q: mentions the failure = %v, want %v", status, failed, applyErr != nil)
			}
		})
	}
}
