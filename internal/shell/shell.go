// Package shell is the one process-spawning primitive shared by every part
// of capi-shell-mcp that shells out (kubectl/OS commands, jq, sshuttle,
// systemctl) — one seam to fake in tests, one place capturing the
// stdout/stderr-cap and exit-code behavior every caller relies on.
package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type Options struct {
	Env []string
	// Input, if non-empty, is written to the child's stdin and then closed.
	Input string
	// MaxOutputBytes caps combined stdout+stderr; 0 means unlimited.
	MaxOutputBytes int
}

type Output struct {
	Stdout string
	Stderr string
}

// ExitError reports a child process that ran but exited non-zero (or whose
// output exceeded MaxOutputBytes), carrying whatever output was captured
// before failure.
type ExitError struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// Execer runs an external command. The production implementation is
// OSExecer; tests fake this interface instead of spawning real processes.
type Execer interface {
	ExecFile(ctx context.Context, file string, args []string, opts Options) (Output, error)
}

type OSExecer struct{}

var _ Execer = OSExecer{}

func (OSExecer) ExecFile(ctx context.Context, file string, args []string, opts Options) (Output, error) {
	cmd := exec.CommandContext(ctx, file, args...)
	cmd.Env = opts.Env
	if opts.Input != "" {
		cmd.Stdin = strings.NewReader(opts.Input)
	}

	var stdout, stderr bytes.Buffer
	if opts.MaxOutputBytes > 0 {
		limit := &sharedLimit{max: opts.MaxOutputBytes}
		cmd.Stdout = &limitedWriter{buf: &stdout, limit: limit}
		cmd.Stderr = &limitedWriter{buf: &stderr, limit: limit}
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	err := cmd.Run()
	if err != nil {
		exitCode := 1
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			exitCode = exitErr.ExitCode()
		}
		return Output{}, &ExitError{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode, Err: err}
	}
	return Output{Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

// sharedLimit tracks remaining budget across a command's combined
// stdout+stderr, matching Node's execFile maxBuffer (a single cap over
// both streams).
type sharedLimit struct {
	max     int
	written int
}

type limitedWriter struct {
	buf   *bytes.Buffer
	limit *sharedLimit
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	w.limit.written += len(p)
	if w.limit.written > w.limit.max {
		return 0, fmt.Errorf("output exceeded maximum of %d bytes", w.limit.max)
	}
	return w.buf.Write(p)
}
