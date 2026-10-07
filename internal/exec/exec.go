// Package exec runs a command against a workload cluster: it writes the
// given kubeconfig to a private temp file, points KUBECONFIG at it (plus
// any OpenStack credential env vars), runs the command, and always cleans
// the temp file up.
package exec

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cwrau/capi-shell-mcp/internal/shell"
)

const (
	// DefaultMaxOutputBytes caps combined stdout+stderr that is returned
	// inline, matching Node's execFile maxBuffer used by the TypeScript
	// implementation this ports.
	DefaultMaxOutputBytes = 10 * 1024 * 1024

	// MaxProcessedOutputBytes is the cap for output that a jq filter shrinks
	// or save_output writes to a file, so nothing of this size reaches the
	// response. jq decodes a document into several times its text size, so it
	// stays well below what a whole fan-out could hold in memory.
	MaxProcessedOutputBytes = 32 * 1024 * 1024

	maxOutputBytes = DefaultMaxOutputBytes
)

type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// WithKubeconfig runs command with KUBECONFIG pointed at a temp file
// containing workloadKubeconfig, plus openstackEnv merged into its
// environment. A non-zero exit or execution error is reported through
// Result (ExitCode != 0), never as a returned error — WithKubeconfig only
// returns an error for a precondition it can check upfront (empty command).
func WithKubeconfig(ctx context.Context, execer shell.Execer, workloadKubeconfig string, openstackEnv map[string]string, command []string, stdin string) (Result, error) {
	if len(command) == 0 {
		return Result{}, errors.New("exec: command must be non-empty")
	}

	tmpFile, err := writeTempKubeconfig(workloadKubeconfig)
	if err != nil {
		return Result{}, fmt.Errorf("exec: writing temp kubeconfig: %w", err)
	}
	defer os.Remove(tmpFile)

	env := os.Environ()
	for k, v := range openstackEnv {
		env = append(env, k+"="+v)
	}
	env = append(env, "KUBECONFIG="+tmpFile)

	out, err := execer.ExecFile(ctx, command[0], command[1:], shell.Options{
		Env:            env,
		Input:          stdin,
		MaxOutputBytes: maxOutputBytes,
	})
	return resultFrom(out, err), nil
}

func resultFrom(out shell.Output, err error) Result {
	if err == nil {
		return Result{Stdout: out.Stdout, Stderr: out.Stderr, ExitCode: 0}
	}
	if exitErr, ok := errors.AsType[*shell.ExitError](err); ok {
		return Result{Stdout: exitErr.Stdout, Stderr: exitErr.Stderr, ExitCode: exitErr.ExitCode}
	}
	return Result{Stderr: err.Error(), ExitCode: 1}
}

// Kubectl runs `kubectl <args>` against workloadKubeconfig with a scrubbed
// environment: only PATH, this process's real HOME and XDG_* variables, a
// private kubectl cache dir and KUBECONFIG pointing at a temp copy of the
// kubeconfig. Nothing else from this process's environment (credentials,
// tokens, KUBE* overrides, …) reaches kubectl.
//
// HOME and XDG_* are deliberate: the kubeconfig's credential plugin (e.g.
// kubelogin) keeps its token cache under them, and without them it would
// find none and start an interactive login. The private dir is the HOME
// only if this process has none.
//
// args must already have passed kubectlargs.Validate — this function never
// runs anything but kubectl. maxBytes caps combined
// stdout+stderr; output beyond it fails the run (see DefaultMaxOutputBytes
// and MaxProcessedOutputBytes).
func Kubectl(ctx context.Context, execer shell.Execer, workloadKubeconfig string, args []string, maxBytes int) (Result, error) {
	dir, err := os.MkdirTemp("", "capi-shell-mcp-kubectl-")
	if err != nil {
		return Result{}, fmt.Errorf("exec: creating private dir: %w", err)
	}
	defer os.RemoveAll(dir)

	kubeconfigPath := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kubeconfigPath, []byte(workloadKubeconfig), 0o600); err != nil {
		return Result{}, fmt.Errorf("exec: writing kubeconfig: %w", err)
	}

	// A non-nil env is essential: a nil Env means "inherit the parent's".
	home := os.Getenv("HOME")
	if home == "" {
		home = dir
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"KUBECONFIG=" + kubeconfigPath,
		"KUBECACHEDIR=" + filepath.Join(dir, "cache"),
	}
	// XDG_* locations may relocate the credential plugin's cache and config
	// away from HOME, so they pass through like HOME does.
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "XDG_") {
			env = append(env, kv)
		}
	}

	out, err := execer.ExecFile(ctx, "kubectl", args, shell.Options{
		Env:            env,
		MaxOutputBytes: maxBytes,
	})
	return resultFrom(out, err), nil
}

func writeTempKubeconfig(contents string) (string, error) {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("capi-shell-mcp-%s.yaml", hex.EncodeToString(suffix)))
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
