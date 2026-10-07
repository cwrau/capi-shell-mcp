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

type kubectlProbe struct {
	file           string
	args           []string
	env            []string
	kubeconfig     string
	maxOutputBytes int
}

func (p *kubectlProbe) ExecFile(_ context.Context, file string, args []string, opts shell.Options) (shell.Output, error) {
	p.file, p.args, p.env, p.maxOutputBytes = file, args, opts.Env, opts.MaxOutputBytes
	for _, kv := range opts.Env {
		if path, ok := strings.CutPrefix(kv, "KUBECONFIG="); ok {
			b, _ := os.ReadFile(path)
			p.kubeconfig = string(b)
		}
	}
	return shell.Output{Stdout: "ok"}, nil
}

func TestKubectlRunsOnlyKubectlWithTheGivenArgs(t *testing.T) {
	probe := &kubectlProbe{}
	result, err := Kubectl(context.Background(), probe, "kc-content", []string{"get", "pods", "-n", "default"}, DefaultMaxOutputBytes)
	if err != nil {
		t.Fatalf("Kubectl: %v", err)
	}
	if probe.file != "kubectl" {
		t.Errorf("file = %q, want kubectl", probe.file)
	}
	if strings.Join(probe.args, " ") != "get pods -n default" {
		t.Errorf("args = %v", probe.args)
	}
	if result.Stdout != "ok" || result.ExitCode != 0 {
		t.Errorf("result = %+v", result)
	}
	if probe.kubeconfig != "kc-content" {
		t.Errorf("kubeconfig seen by kubectl = %q", probe.kubeconfig)
	}
}

func TestKubectlScrubsTheEnvironment(t *testing.T) {
	t.Setenv("CAPI_TEST_SECRET", "leak-me")
	t.Setenv("KUBECTL_PLUGINS_PATH", "/evil")
	t.Setenv("OS_PASSWORD", "hunter2")

	probe := &kubectlProbe{}
	if _, err := Kubectl(context.Background(), probe, "kc", []string{"version", "--client"}, DefaultMaxOutputBytes); err != nil {
		t.Fatalf("Kubectl: %v", err)
	}

	if probe.env == nil {
		t.Fatal("env is nil — exec would inherit the whole parent environment")
	}
	keys := map[string]bool{}
	for _, kv := range probe.env {
		k, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, "XDG_") {
			keys[k] = true
		}
		if strings.Contains(kv, "leak-me") || strings.Contains(kv, "hunter2") || strings.Contains(kv, "/evil") {
			t.Errorf("env leaked a parent variable: %q", kv)
		}
	}
	for _, want := range []string{"PATH", "HOME", "KUBECONFIG", "KUBECACHEDIR"} {
		if !keys[want] {
			t.Errorf("env missing %s: %v", want, probe.env)
		}
	}
	if len(keys) != 4 {
		t.Errorf("env keys = %v, want exactly PATH/HOME/KUBECONFIG/KUBECACHEDIR besides XDG_*", keys)
	}
}

func TestKubectlRemovesItsPrivateDir(t *testing.T) {
	before := tempFileCount(t)
	if _, err := Kubectl(context.Background(), &kubectlProbe{}, "kc", []string{"api-versions"}, DefaultMaxOutputBytes); err != nil {
		t.Fatalf("Kubectl: %v", err)
	}
	if after := tempFileCount(t); after != before {
		t.Errorf("temp entries before=%d after=%d, want unchanged", before, after)
	}
}

func TestKubectlReportsNonZeroExit(t *testing.T) {
	execer := &fakeExecer{err: &shell.ExitError{ExitCode: 1, Stderr: "boom", Err: os.ErrInvalid}}
	result, err := Kubectl(context.Background(), execer, "kc", []string{"get", "pods"}, DefaultMaxOutputBytes)
	if err != nil {
		t.Fatalf("Kubectl: %v", err)
	}
	if result.ExitCode != 1 || result.Stderr != "boom" {
		t.Errorf("result = %+v", result)
	}
}

func TestKubectlPassesTheOutputCapItIsGivenToTheExecer(t *testing.T) {
	for _, limit := range []int{DefaultMaxOutputBytes, MaxProcessedOutputBytes, 1234} {
		probe := &kubectlProbe{}
		if _, err := Kubectl(context.Background(), probe, "kc", []string{"get", "pods"}, limit); err != nil {
			t.Fatalf("Kubectl: %v", err)
		}
		if probe.maxOutputBytes != limit {
			t.Errorf("execer saw cap %d, want %d", probe.maxOutputBytes, limit)
		}
	}
	if MaxProcessedOutputBytes <= DefaultMaxOutputBytes {
		t.Errorf("the cap for output that is filtered or saved (%d) must exceed the inline cap (%d)", MaxProcessedOutputBytes, DefaultMaxOutputBytes)
	}
}

func kubectlEnvValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

func TestKubectlPassesThroughTheRealHome(t *testing.T) {
	t.Setenv("HOME", "/home/real-user")
	probe := &kubectlProbe{}
	if _, err := Kubectl(context.Background(), probe, "kc", []string{"get", "pods"}, DefaultMaxOutputBytes); err != nil {
		t.Fatalf("Kubectl: %v", err)
	}
	if got, _ := kubectlEnvValue(probe.env, "HOME"); got != "/home/real-user" {
		t.Errorf("HOME = %q, want the real one so a credential plugin finds its token cache", got)
	}
}

func TestKubectlFallsBackToItsPrivateDirWithoutAHome(t *testing.T) {
	t.Setenv("HOME", "")
	probe := &kubectlProbe{}
	if _, err := Kubectl(context.Background(), probe, "kc", []string{"get", "pods"}, DefaultMaxOutputBytes); err != nil {
		t.Fatalf("Kubectl: %v", err)
	}
	home, _ := kubectlEnvValue(probe.env, "HOME")
	if !strings.Contains(home, "capi-shell-mcp-kubectl-") {
		t.Errorf("HOME = %q, want the private dir when the process has no HOME", home)
	}
}

func TestKubectlPassesThroughXDGVariablesOnly(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/cache")
	t.Setenv("XDG_CONFIG_HOME", "/config")
	t.Setenv("NOT_XDG_VARIABLE", "leak-me")

	probe := &kubectlProbe{}
	if _, err := Kubectl(context.Background(), probe, "kc", []string{"get", "pods"}, DefaultMaxOutputBytes); err != nil {
		t.Fatalf("Kubectl: %v", err)
	}
	if got, _ := kubectlEnvValue(probe.env, "XDG_CACHE_HOME"); got != "/cache" {
		t.Errorf("XDG_CACHE_HOME = %q, want it passed through", got)
	}
	if got, _ := kubectlEnvValue(probe.env, "XDG_CONFIG_HOME"); got != "/config" {
		t.Errorf("XDG_CONFIG_HOME = %q, want it passed through", got)
	}
	if _, ok := kubectlEnvValue(probe.env, "NOT_XDG_VARIABLE"); ok {
		t.Errorf("env = %v, want non-XDG variables still scrubbed", probe.env)
	}
}
