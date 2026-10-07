package server

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func textResult(text string, isError bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: isError}
}

// resolveClusterPatterns resolves in against every currently known
// cluster, lazily — the shared pattern language every one of our tools
// uses, matching the embedded (kubernetes-mcp-server) tools' convention.
// For non-strict (read-only) callers an omitted selection (nil patterns)
// means every known cluster, while an empty array means none.
func (s *ToolServer) resolveClusterPatterns(ctx context.Context, patterns []string, strict bool) ([]string, error) {
	if !strict && patterns == nil {
		known, err := s.allKnownClusters(ctx)
		if err != nil {
			return nil, err
		}
		targets := make([]string, len(known))
		for i, c := range known {
			targets[i] = capi.FormatTarget(c)
		}
		return targets, nil
	}
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

// GetClusterKubeconfig implements the get_cluster_kubeconfig tool.
func (s *ToolServer) GetClusterKubeconfig(ctx context.Context, in ClusterFilterInput) (*mcp.CallToolResult, error) {
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
			results[i] = kubeconfigResult{Cluster: target, Kubeconfig: adminKC}
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

type clusterID struct {
	Kubeconfig string `json:"kubeconfig"`
	Context    string `json:"context"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
}
