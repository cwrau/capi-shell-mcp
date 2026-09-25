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

	"github.com/cwrau/capi-shell-mcp/internal/shell"
)

// maxOutputBytes caps combined stdout+stderr, matching Node's execFile
// maxBuffer used by the TypeScript implementation this ports.
const maxOutputBytes = 10 * 1024 * 1024

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
	if err == nil {
		return Result{Stdout: out.Stdout, Stderr: out.Stderr, ExitCode: 0}, nil
	}

	if exitErr, ok := errors.AsType[*shell.ExitError](err); ok {
		return Result{Stdout: exitErr.Stdout, Stderr: exitErr.Stderr, ExitCode: exitErr.ExitCode}, nil
	}
	return Result{Stderr: err.Error(), ExitCode: 1}, nil
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
