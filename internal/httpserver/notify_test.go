package httpserver

import (
	"context"
	"testing"

	"github.com/cwrau/capi-shell-mcp/internal/shell"
)

type recordingExecer struct {
	calls [][]string
}

func (e *recordingExecer) ExecFile(_ context.Context, file string, args []string, _ shell.Options) (shell.Output, error) {
	e.calls = append(e.calls, append([]string{file}, args...))
	return shell.Output{}, nil
}

func TestNotifySystemdNoopWhenNotifySocketUnset(t *testing.T) {
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
