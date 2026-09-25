package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
	"github.com/cwrau/capi-shell-mcp/internal/exec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func textResult(text string, isError bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: isError}
}

// resolveClusterPatterns resolves in against every currently known
// cluster, lazily — the shared pattern language every one of our tools
// uses, matching the embedded (kubernetes-mcp-server) tools' convention.
func (s *ToolServer) resolveClusterPatterns(ctx context.Context, patterns []string, strict bool) ([]string, error) {
	return capi.ResolveClusterPatterns(patterns, func() ([]capi.CAPICluster, error) {
		return s.allKnownClusters(ctx)
	}, strict)
}

// listClustersEntry is a CAPICluster plus its composite target string — the
// identifier every tool's "clusters" parameter (ours and the embedded
// tools') accepts, so the LLM never has to build it by hand.
type listClustersEntry struct {
	capi.CAPICluster
	Target string `json:"target"`
}

// ListClusters implements the list_clusters tool.
func (s *ToolServer) ListClusters(ctx context.Context, in ClusterFilterInput) (*mcp.CallToolResult, error) {
	resolved, err := s.allResolvedClusters(ctx)
	if err != nil {
		return nil, err
	}

	if len(in.Clusters) > 0 {
		known := make([]capi.CAPICluster, len(resolved))
		for i, r := range resolved {
			known[i] = r.Cluster
		}
		matched, err := capi.ResolveClusterPatterns(in.Clusters, func() ([]capi.CAPICluster, error) { return known, nil }, false)
		if err != nil {
			return textResult(err.Error(), true), nil
		}
		matchSet := make(map[string]bool, len(matched))
		for _, t := range matched {
			matchSet[t] = true
		}
		kept := make([]resolvedCluster, 0, len(resolved))
		for _, r := range resolved {
			if matchSet[capi.FormatTarget(r.Cluster)] {
				kept = append(kept, r)
			}
		}
		resolved = kept
	}

	clusters := make([]listClustersEntry, len(resolved))
	for i, r := range resolved {
		clusters[i] = listClustersEntry{CAPICluster: r.Cluster, Target: capi.FormatTarget(r.Cluster)}
	}
	b, err := json.MarshalIndent(clusters, "", "  ")
	if err != nil {
		return nil, err
	}
	return textResult(string(b), false), nil
}

type kubeconfigResult struct {
	Cluster    string `json:"cluster"`
	Kubeconfig string `json:"kubeconfig,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (s *ToolServer) getClusterKubeconfigFanOut(ctx context.Context, in ClusterFilterInput, readonly bool) (*mcp.CallToolResult, error) {
	targets, err := s.resolveClusterPatterns(ctx, in.Clusters, false)
	if err != nil {
		return textResult(err.Error(), true), nil
	}

	results := make([]kubeconfigResult, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()
			adminKC, err := s.ResolveTarget(ctx, target)
			if err != nil {
				results[i] = kubeconfigResult{Cluster: target, Error: err.Error()}
				return
			}
			if !readonly {
				results[i] = kubeconfigResult{Cluster: target, Kubeconfig: adminKC}
				return
			}
			roKC, err := s.readOnlyKubeconfig(ctx, adminKC)
			if err != nil {
				results[i] = kubeconfigResult{Cluster: target, Error: err.Error()}
				return
			}
			results[i] = kubeconfigResult{Cluster: target, Kubeconfig: roKC}
		}(i, target)
	}
	wg.Wait()

	b, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return nil, err
	}
	isError := false
	for _, r := range results {
		if r.Error != "" {
			isError = true
			break
		}
	}
	return textResult(string(b), isError), nil
}

// GetClusterKubeconfig implements the get_cluster_kubeconfig tool.
func (s *ToolServer) GetClusterKubeconfig(ctx context.Context, in ClusterFilterInput) (*mcp.CallToolResult, error) {
	return s.getClusterKubeconfigFanOut(ctx, in, false)
}

// GetClusterKubeconfigReadonly implements the get_cluster_kubeconfig_readonly tool.
func (s *ToolServer) GetClusterKubeconfigReadonly(ctx context.Context, in ClusterFilterInput) (*mcp.CallToolResult, error) {
	return s.getClusterKubeconfigFanOut(ctx, in, true)
}

type clusterID struct {
	Kubeconfig string `json:"kubeconfig"`
	Context    string `json:"context"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
}

type clusterExecResult struct {
	Cluster  clusterID `json:"cluster"`
	Stdout   *string   `json:"stdout"`
	Stderr   *string   `json:"stderr"`
	ExitCode *int      `json:"exit_code"`
	Error    *string   `json:"error"`
}

func clusterErrResult(id clusterID, err error) clusterExecResult {
	msg := err.Error()
	return clusterExecResult{Cluster: id, Error: &msg}
}

func nonEmptyPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// execOneTarget runs command against one exact target, isolating any
// error into the returned result rather than propagating it, so one
// cluster's failure never fails the whole fan-out call.
func (s *ToolServer) execOneTarget(ctx context.Context, target string, command []string, stdin string, readonly bool) clusterExecResult {
	kubeconfigName, contextName, namespace, name, err := capi.ParseTarget(target)
	if err != nil {
		return clusterErrResult(clusterID{}, err)
	}
	id := clusterID{Kubeconfig: kubeconfigName, Context: contextName, Namespace: namespace, Name: name}

	src, ok := s.cfg.Kubeconfigs[kubeconfigName]
	if !ok {
		return clusterErrResult(id, fmt.Errorf("unknown kubeconfig: %s", kubeconfigName))
	}

	kc, err := s.cachedKubeconfig(ctx, src, contextName, namespace, name)
	if err != nil {
		return clusterErrResult(id, err)
	}

	var osEnv map[string]string
	if readonly {
		roKC, err := s.readOnlyKubeconfig(ctx, kc)
		if err != nil {
			return clusterErrResult(id, err)
		}
		kc = roKC
	} else {
		clients, err := s.clientsForContext(src, contextName)
		if err != nil {
			return clusterErrResult(id, err)
		}
		osEnv, err = capi.FetchOpenStackEnv(ctx, clients.Dynamic, clients.Core, namespace, name)
		if err != nil {
			return clusterErrResult(id, err)
		}
	}

	r, err := exec.WithKubeconfig(ctx, s.execer, kc, osEnv, command, stdin)
	if err != nil {
		return clusterErrResult(id, err)
	}
	exitCode := r.ExitCode
	return clusterExecResult{Cluster: id, Stdout: nonEmptyPtr(r.Stdout), Stderr: nonEmptyPtr(r.Stderr), ExitCode: &exitCode}
}

func (s *ToolServer) execInClusterFanOut(ctx context.Context, in ExecInput, readonly bool) (*mcp.CallToolResult, error) {
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
			results[i] = s.execOneTarget(ctx, target, in.Command, in.Stdin, readonly)
		}(i, target)
	}
	wg.Wait()

	b, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return nil, err
	}
	isError := false
	for _, r := range results {
		if r.Error != nil || (r.ExitCode != nil && *r.ExitCode != 0) {
			isError = true
			break
		}
	}
	return textResult(string(b), isError), nil
}

// ExecInCluster implements the exec_in_cluster tool.
func (s *ToolServer) ExecInCluster(ctx context.Context, in ExecInput) (*mcp.CallToolResult, error) {
	return s.execInClusterFanOut(ctx, in, false)
}

// ExecInClusterReadonly implements the exec_in_cluster_readonly tool.
func (s *ToolServer) ExecInClusterReadonly(ctx context.Context, in ExecInput) (*mcp.CallToolResult, error) {
	return s.execInClusterFanOut(ctx, in, true)
}
