package server

import (
	"context"
	"fmt"
	"time"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
)

// ListTargets returns the composite target string (see capi.FormatTarget)
// for every currently-known CAPI cluster — the identifier embedded
// (kubernetes-mcp-server) tools accept for their cluster-selector
// parameter.
func (s *ToolServer) ListTargets(ctx context.Context) ([]string, error) {
	resolved, err := s.allResolvedClusters(ctx)
	if err != nil {
		return nil, err
	}
	targets := make([]string, len(resolved))
	for i, r := range resolved {
		targets[i] = capi.FormatTarget(r.Cluster)
	}
	return targets, nil
}

// allKnownClusters is allResolvedClusters flattened to bare capi.CAPICluster
// values, for capi.ResolveClusterPatterns' prefix matching.
func (s *ToolServer) allKnownClusters(ctx context.Context) ([]capi.CAPICluster, error) {
	resolved, err := s.allResolvedClusters(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]capi.CAPICluster, len(resolved))
	for i, r := range resolved {
		out[i] = r.Cluster
	}
	return out, nil
}

// ResolveTarget parses a composite target string and returns the admin
// kubeconfig YAML for that workload cluster, via the same cached
// fetch/transform/proxy pipeline our own tools use.
func (s *ToolServer) ResolveTarget(ctx context.Context, target string) (string, error) {
	kubeconfigName, contextName, namespace, name, err := capi.ParseTarget(target)
	if err != nil {
		return "", err
	}
	src, ok := s.config().Kubeconfigs[kubeconfigName]
	if !ok {
		return "", fmt.Errorf("unknown kubeconfig: %s", kubeconfigName)
	}
	return s.cachedKubeconfig(ctx, src, contextName, namespace, name)
}

// ResolveTargetReadonly is ResolveTarget, but returns a freshly built
// read-only kubeconfig for the cluster instead of the admin one.
func (s *ToolServer) ResolveTargetReadonly(ctx context.Context, target string) (string, error) {
	adminKC, err := s.ResolveTarget(ctx, target)
	if err != nil {
		return "", err
	}
	return s.readOnlyKubeconfig(ctx, adminKC)
}

// ReadonlyTTL is how long a client built from a read-only kubeconfig is kept.
func (s *ToolServer) ReadonlyTTL() time.Duration { return s.kubeconfigTTL() }
