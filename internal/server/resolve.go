package server

import (
	"context"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
	"github.com/cwrau/capi-shell-mcp/internal/config"
)

// allResolvedClusters lists every CAPI cluster across every configured
// kubeconfig, caching per kubeconfig+context. Callers that want a subset
// filter the result themselves via capi.ResolveClusterPatterns, rather
// than this function taking a filter argument — there's exactly one way
// to select clusters (the "clusters" pattern language), used uniformly
// everywhere.
func (s *ToolServer) allResolvedClusters(ctx context.Context) ([]resolvedCluster, error) {
	var result []resolvedCluster
	for _, src := range s.sortedKubeconfigs() {
		contexts, err := s.resolveContexts(src)
		if err != nil {
			return nil, err
		}
		for _, contextName := range contexts {
			clusters, err := s.listClustersCached(ctx, src, contextName)
			if err != nil {
				return nil, err
			}
			for _, c := range clusters {
				result = append(result, resolvedCluster{Cluster: c, Source: src})
			}
		}
	}
	return result, nil
}

func (s *ToolServer) listClustersCached(ctx context.Context, src config.KubeconfigSource, contextName string) ([]capi.CAPICluster, error) {
	key := capi.ClusterListKey(src.Name, contextName)
	if cached, ok := s.store.ClusterList.Get(key); ok {
		return cached, nil
	}
	clients, err := s.clientsForContext(src, contextName)
	if err != nil {
		return nil, err
	}
	clusters, err := capi.ListClustersForContext(ctx, clients.Dynamic, src.Name, contextName, s.cfg.CustomFields)
	if err != nil {
		return nil, err
	}
	s.store.ClusterList.Set(key, clusters)
	return clusters, nil
}
