package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
	"github.com/cwrau/capi-shell-mcp/internal/config"
	"github.com/cwrau/capi-shell-mcp/internal/exec"
	"github.com/cwrau/capi-shell-mcp/internal/kubectlargs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// clusterExecResult is one cluster's outcome. Every field but Cluster is
// omitted when empty, and exit_code only appears when non-zero, matching
// upstream kubernetes-mcp-server's convention of not emitting empty values.
type clusterExecResult struct {
	Cluster     clusterID `json:"cluster"`
	Stdout      string    `json:"stdout,omitempty"`
	StdoutFile  string    `json:"stdout_file,omitempty"`
	StdoutBytes int       `json:"stdout_bytes,omitempty"`
	Stderr      string    `json:"stderr,omitempty"`
	ExitCode    int       `json:"exit_code,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// hasNothingToReport is true for a cluster that ran fine and produced no
// stdout. Its stderr is deliberately ignored: on a successful exit with no
// stdout it is noise like kubectl's "No resources found".
func (r clusterExecResult) hasNothingToReport() bool {
	return r.Error == "" && r.ExitCode == 0 && r.Stdout == "" && r.StdoutFile == ""
}

func clusterErrResult(id clusterID, err error) clusterExecResult {
	return clusterExecResult{Cluster: id, Error: err.Error()}
}

// finishExecResults drops clusters with nothing to report and renders the
// rest. Errors and non-zero exits always stay, so an absent cluster
// reliably means "ran fine and printed nothing". The result is always a
// JSON array, "[]" when nothing is left.
func finishExecResults(results []clusterExecResult) (*mcp.CallToolResult, error) {
	kept := make([]clusterExecResult, 0, len(results))
	isError := false
	for _, r := range results {
		if r.hasNothingToReport() {
			continue
		}
		if r.Error != "" || r.ExitCode != 0 {
			isError = true
		}
		kept = append(kept, r)
	}
	b, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return nil, err
	}
	return textResult(string(b), isError), nil
}

// execTarget is everything resolved about one exact target before a
// command can run against it.
type execTarget struct {
	id  clusterID
	src config.KubeconfigSource
	kc  string
}

// resolveExecTarget parses target and fetches its (cached) admin
// kubeconfig through the shared fetch/transform/proxy pipeline.
func (s *ToolServer) resolveExecTarget(ctx context.Context, target string) (execTarget, error) {
	kubeconfigName, contextName, namespace, name, err := capi.ParseTarget(target)
	if err != nil {
		return execTarget{}, err
	}
	id := clusterID{Kubeconfig: kubeconfigName, Context: contextName, Namespace: namespace, Name: name}

	src, ok := s.cfg.Kubeconfigs[kubeconfigName]
	if !ok {
		return execTarget{id: id}, fmt.Errorf("unknown kubeconfig: %s", kubeconfigName)
	}
	kc, err := s.cachedKubeconfig(ctx, src, contextName, namespace, name)
	if err != nil {
		return execTarget{id: id}, err
	}
	return execTarget{id: id, src: src, kc: kc}, nil
}

// execAdminOneTarget runs an arbitrary command with the cluster's admin
// kubeconfig and OpenStack credentials, isolating any error into the
// returned result so one cluster's failure never fails the whole call.
func (s *ToolServer) execAdminOneTarget(ctx context.Context, target string, command []string, stdin string) clusterExecResult {
	t, err := s.resolveExecTarget(ctx, target)
	if err != nil {
		return clusterErrResult(t.id, err)
	}
	clients, err := s.clientsForContext(t.src, t.id.Context)
	if err != nil {
		return clusterErrResult(t.id, err)
	}
	osEnv, err := capi.FetchOpenStackEnv(ctx, clients.Dynamic, clients.Core, t.id.Namespace, t.id.Name)
	if err != nil {
		return clusterErrResult(t.id, err)
	}
	r, err := exec.WithKubeconfig(ctx, s.execer, t.kc, osEnv, command, stdin)
	if err != nil {
		return clusterErrResult(t.id, err)
	}
	return clusterExecResult{Cluster: t.id, Stdout: r.Stdout, Stderr: r.Stderr, ExitCode: r.ExitCode}
}

// ExecInCluster implements the exec_in_cluster tool: an arbitrary command,
// so every target must be a full exact path.
func (s *ToolServer) ExecInCluster(ctx context.Context, in ExecInput) (*mcp.CallToolResult, error) {
	if len(in.Command) == 0 {
		return nil, errors.New("exec_in_cluster: command must be non-empty")
	}
	if len(in.Clusters) == 0 {
		return textResult("clusters is required and must be a non-empty array of full kubeconfig/context/namespace/name paths", true), nil
	}
	targets, err := capi.ResolveClusterPatterns(in.Clusters, nil, true)
	if err != nil {
		return textResult(err.Error(), true), nil
	}

	results := make([]clusterExecResult, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()
			results[i] = s.execAdminOneTarget(ctx, target, in.Command, in.Stdin)
		}(i, target)
	}
	wg.Wait()
	return finishExecResults(results)
}

// execReadonlyOneTarget runs `kubectl <args>` with an impersonating
// read-only kubeconfig, then applies the optional jq filter and
// save-to-file post-processing.
func (s *ToolServer) execReadonlyOneTarget(ctx context.Context, target string, args []string, post *postProcessor) clusterExecResult {
	t, err := s.resolveExecTarget(ctx, target)
	if err != nil {
		return clusterErrResult(t.id, err)
	}
	roKC, err := s.readOnlyKubeconfig(ctx, t.kc)
	if err != nil {
		return clusterErrResult(t.id, err)
	}
	r, err := exec.Kubectl(ctx, s.execer, roKC, args, post.maxInputBytes())
	if err != nil {
		return clusterErrResult(t.id, err)
	}
	res := clusterExecResult{Cluster: t.id, Stdout: r.Stdout, Stderr: r.Stderr, ExitCode: r.ExitCode}
	post.apply(ctx, &res)
	return res
}

// ExecInClusterReadonly implements the exec_in_cluster_readonly tool. It
// can only run kubectl, with arguments restricted to a read-only allowlist
// (see kubectlargs), so unlike exec_in_cluster it is safe to point at many
// clusters at once via a prefix or an omitted "clusters".
func (s *ToolServer) ExecInClusterReadonly(ctx context.Context, in ReadonlyExecInput) (*mcp.CallToolResult, error) {
	if err := kubectlargs.Validate(in.Args); err != nil {
		return textResult("args rejected: "+err.Error(), true), nil
	}
	post, err := newPostProcessor(in.JQ, in.SaveOutput, s.outputDir())
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	targets, err := s.resolveClusterPatterns(ctx, in.Clusters, false)
	if err != nil {
		return textResult(err.Error(), true), nil
	}

	results := make([]clusterExecResult, len(targets))
	var wg sync.WaitGroup
	slots := make(chan struct{}, maxParallelReadonly)
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			results[i] = s.execReadonlyOneTarget(ctx, target, in.Args, post)
		}(i, target)
	}
	wg.Wait()
	return finishExecResults(results)
}

// maxParallelReadonly bounds how many clusters one exec_in_cluster_readonly
// call works on at once. A prefix or an omitted "clusters" can select every
// known cluster, and each run holds up to exec.MaxProcessedOutputBytes of
// output plus, with jq, the decoded document several times over.
const maxParallelReadonly = 8
