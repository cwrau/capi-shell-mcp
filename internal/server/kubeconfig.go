package server

import (
	"context"
	"time"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
	"github.com/cwrau/capi-shell-mcp/internal/config"
)

func (s *ToolServer) kubeconfigTTL() time.Duration {
	return time.Duration(s.cfg.Cache.KubeconfigTTLSeconds) * time.Second
}

// cachedKubeconfig fetches (or returns the cached copy of) the admin
// kubeconfig for one workload cluster, applying configured transforms and
// ensuring an sshuttle tunnel if its API server needs one.
//
// sshuttleHost is resolved before the cache check so a cache hit still
// knows whether to refresh an existing tunnel's TTL; on a cache miss, the
// (comparatively expensive) API-server-restriction check is only made at
// all if an sshuttleHost is configured.
func (s *ToolServer) cachedKubeconfig(ctx context.Context, src config.KubeconfigSource, contextName, namespace, clusterName string) (string, error) {
	sshuttleHost := src.SshuttleHost
	if sshuttleHost == "" {
		sshuttleHost = s.cfg.SshuttleHost
	}
	ttl := s.kubeconfigTTL()
	key := capi.KubeconfigKey(src.Name, contextName, namespace, clusterName)

	if cached, ok := s.store.Kubeconfig.Get(key); ok {
		if cached.ProxyTarget != nil {
			if err := s.proxyMgr.EnsureProxy(ctx, cached.ProxyTarget.APIServerIP, cached.ProxyTarget.APIServerPort, cached.ProxyTarget.SshuttleHost, ttl); err != nil {
				return "", err
			}
		}
		return cached.Kubeconfig, nil
	}

	clients, err := s.clientsForContext(src, contextName)
	if err != nil {
		return "", err
	}

	kc, err := capi.FetchWorkloadKubeconfig(ctx, clients.Core, namespace, clusterName)
	if err != nil {
		return "", err
	}
	if s.cfg.Transforms != nil {
		kc, err = capi.ApplyKubeconfigTransform(kc, *s.cfg.Transforms)
		if err != nil {
			return "", err
		}
	}

	var proxyTarget *capi.ProxyTarget
	if sshuttleHost != "" {
		host, port, hasInfo, err := capi.FetchAPIServerInfo(ctx, clients.Dynamic, namespace, clusterName)
		if err != nil {
			return "", err
		}
		if hasInfo {
			if err := s.proxyMgr.EnsureProxy(ctx, host, port, sshuttleHost, ttl); err != nil {
				return "", err
			}
			proxyTarget = &capi.ProxyTarget{SshuttleHost: sshuttleHost, APIServerIP: host, APIServerPort: port}
		}
	}

	s.store.Kubeconfig.Set(key, capi.CachedKubeconfig{Kubeconfig: kc, ProxyTarget: proxyTarget})
	return kc, nil
}

// readOnlyKubeconfig mints a read-only kubeconfig for the workload cluster
// identified by adminKC, built from a client derived from adminKC itself
// (not the kubeconfig entry it was discovered through) — the read-only
// RBAC/token live on the workload cluster.
func (s *ToolServer) readOnlyKubeconfig(ctx context.Context, adminKC string) (string, error) {
	core, rbac, err := s.clientsFromReadonlySource(adminKC)
	if err != nil {
		return "", err
	}
	return capi.CreateReadOnlyKubeconfig(ctx, core, rbac, adminKC, int64(s.kubeconfigTTL().Seconds()))
}
