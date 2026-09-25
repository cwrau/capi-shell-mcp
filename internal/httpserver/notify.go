package httpserver

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cwrau/capi-shell-mcp/internal/shell"
)

// notifySystemd shells out to systemd-notify with args (e.g. "--ready",
// or "--stopping", "--status=..."), but only when NOTIFY_SOCKET is set —
// i.e. only when actually running under a systemd unit with Type=notify.
// Failures are logged, never fatal.
func notifySystemd(execer shell.Execer, args ...string) {
	if os.Getenv("NOTIFY_SOCKET") == "" {
		return
	}
	if _, err := execer.ExecFile(context.Background(), "systemd-notify", args, shell.Options{}); err != nil {
		fmt.Fprintf(os.Stderr, "systemd-notify %s failed: %v\n", strings.Join(args, " "), err)
	}
}
