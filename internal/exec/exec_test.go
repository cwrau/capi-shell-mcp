package exec

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cwrau/capi-shell-mcp/internal/shell"
)

type fakeExecer struct {
	output   shell.Output
	err      error
	gotFile  string
	gotArgs  []string
	gotOpts  shell.Options
	unlinked []string
}

func (f *fakeExecer) ExecFile(_ context.Context, file string, args []string, opts shell.Options) (shell.Output, error) {
	f.gotFile = file
	f.gotArgs = args
	f.gotOpts = opts
	return f.output, f.err
}

func tempFileCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "capi-shell-mcp-") {
			n++
		}
	}
	return n
}

func TestExecWithKubeconfigReturnsStdoutOnSuccess(t *testing.T) {
	execer := &fakeExecer{output: shell.Output{Stdout: "hello\n"}}
	result, err := WithKubeconfig(context.Background(), execer, "kc-content", nil, []string{"echo", "hello"}, "")
	if err != nil {
		t.Fatalf("WithKubeconfig: %v", err)
	}
	if result.Stdout != "hello\n" || result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestExecWithKubeconfigReturnsNonZeroExitCodeOnFailure(t *testing.T) {
	execer := &fakeExecer{err: &shell.ExitError{Stdout: "", Stderr: "error msg", ExitCode: 1, Err: os.ErrInvalid}}
	result, err := WithKubeconfig(context.Background(), execer, "kc-content", nil, []string{"false"}, "")
	if err != nil {
		t.Fatalf("WithKubeconfig: %v", err)
	}
	if result.ExitCode != 1 || result.Stderr != "error msg" {
		t.Fatalf("result = %+v", result)
	}
}

func TestExecWithKubeconfigDeletesTempFileOnSuccess(t *testing.T) {
	before := tempFileCount(t)
	execer := &fakeExecer{output: shell.Output{}}
	if _, err := WithKubeconfig(context.Background(), execer, "kc", nil, []string{"true"}, ""); err != nil {
		t.Fatalf("WithKubeconfig: %v", err)
	}
	if after := tempFileCount(t); after != before {
		t.Errorf("temp file count = %d, want %d (unchanged)", after, before)
	}
}

func TestExecWithKubeconfigDeletesTempFileOnFailure(t *testing.T) {
	before := tempFileCount(t)
	execer := &fakeExecer{err: &shell.ExitError{ExitCode: 2, Err: os.ErrInvalid}}
	if _, err := WithKubeconfig(context.Background(), execer, "kc", nil, []string{"false"}, ""); err != nil {
		t.Fatalf("WithKubeconfig: %v", err)
	}
	if after := tempFileCount(t); after != before {
		t.Errorf("temp file count = %d, want %d (unchanged)", after, before)
	}
}

func TestExecWithKubeconfigErrorsWhenCommandEmpty(t *testing.T) {
	_, err := WithKubeconfig(context.Background(), &fakeExecer{}, "kc", nil, []string{}, "")
	if err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("err = %v, want mention of 'non-empty'", err)
	}
}

func TestExecWithKubeconfigSetsKubeconfigAndOpenStackEnvVars(t *testing.T) {
	execer := &fakeExecer{output: shell.Output{}}
	_, err := WithKubeconfig(context.Background(), execer, "kc", map[string]string{"OS_AUTH_URL": "https://ks.example.com"}, []string{"true"}, "")
	if err != nil {
		t.Fatalf("WithKubeconfig: %v", err)
	}
	var sawAuthURL, sawKubeconfig bool
	for _, kv := range execer.gotOpts.Env {
		if kv == "OS_AUTH_URL=https://ks.example.com" {
			sawAuthURL = true
		}
		if strings.HasPrefix(kv, "KUBECONFIG=") && strings.Contains(kv, "capi-shell-mcp-") {
			sawKubeconfig = true
		}
	}
	if !sawAuthURL {
		t.Errorf("env = %v, missing OS_AUTH_URL", execer.gotOpts.Env)
	}
	if !sawKubeconfig {
		t.Errorf("env = %v, missing matching KUBECONFIG", execer.gotOpts.Env)
	}
}
