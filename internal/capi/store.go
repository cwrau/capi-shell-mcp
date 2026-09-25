package capi

import (
	"time"

	"github.com/cwrau/capi-shell-mcp/internal/cache"
)

// ProxyTarget identifies the sshuttle tunnel (if any) a cached kubeconfig
// depends on to reach its workload cluster's API server.
type ProxyTarget struct {
	SshuttleHost  string
	APIServerIP   string
	APIServerPort string
}

// CachedKubeconfig is a cached admin kubeconfig, plus the proxy tunnel
// target it needs (nil if the cluster's API server is directly routable).
type CachedKubeconfig struct {
	Kubeconfig  string
	ProxyTarget *ProxyTarget
}

// Store holds the two process-lifetime caches shared across every MCP
// session: the CAPI cluster list per kubeconfig+context, and fetched admin
// kubeconfigs. Read-only kubeconfigs/tokens are deliberately never cached
// — see CreateReadOnlyKubeconfig.
type Store struct {
	ClusterList *cache.TTLCache[string, []CAPICluster]
	Kubeconfig  *cache.TTLCache[string, CachedKubeconfig]
}

func NewStore(clusterListTTL, kubeconfigTTL time.Duration) *Store {
	return &Store{
		ClusterList: cache.New[string, []CAPICluster](clusterListTTL),
		Kubeconfig:  cache.New[string, CachedKubeconfig](kubeconfigTTL),
	}
}

func ClusterListKey(kubeconfigName, context string) string {
	return kubeconfigName + ":" + context
}

func KubeconfigKey(kubeconfigName, context, namespace, clusterName string) string {
	return kubeconfigName + ":" + context + ":" + namespace + ":" + clusterName
}
