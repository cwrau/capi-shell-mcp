package shell

import (
	"context"
	"errors"
	"testing"
)

func TestExecFileCapturesStdoutAndStderrOnSuccess(t *testing.T) {
	out, err := (OSExecer{}).ExecFile(context.Background(), "sh", []string{"-c", "printf hello; printf world >&2"}, Options{})
	if err != nil {
		t.Fatalf("ExecFile: %v", err)
	}
	if out.Stdout != "hello" {
		t.Errorf("Stdout = %q, want %q", out.Stdout, "hello")
	}
	if out.Stderr != "world" {
		t.Errorf("Stderr = %q, want %q", out.Stderr, "world")
	}
}

func TestExecFileReturnsExitErrorWithCodeAndPartialOutput(t *testing.T) {
	_, err := (OSExecer{}).ExecFile(context.Background(), "sh", []string{"-c", "printf partial; exit 3"}, Options{})
	if err == nil {
		t.Fatal("expected error for nonzero exit")
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error is not *ExitError: %v", err)
	}
	if exitErr.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", exitErr.ExitCode)
	}
	if exitErr.Stdout != "partial" {
		t.Errorf("Stdout = %q, want %q", exitErr.Stdout, "partial")
	}
}

func TestExecFileWritesInputToStdin(t *testing.T) {
	out, err := (OSExecer{}).ExecFile(context.Background(), "cat", nil, Options{Input: "piped-in"})
	if err != nil {
		t.Fatalf("ExecFile: %v", err)
	}
	if out.Stdout != "piped-in" {
		t.Errorf("Stdout = %q, want %q", out.Stdout, "piped-in")
	}
}

func TestExecFileErrorsWhenOutputExceedsMaxOutputBytes(t *testing.T) {
	_, err := (OSExecer{}).ExecFile(context.Background(), "sh", []string{"-c", "printf 0123456789"}, Options{MaxOutputBytes: 5})
	if err == nil {
		t.Fatal("expected error when output exceeds MaxOutputBytes")
	}
}
