package capi

import (
	"sync"
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
// kubeconfigs. Read-only kubeconfigs are deliberately never cached
// — see ReadOnlyKubeconfig.
type Store struct {
	ClusterList *cache.TTLCache[string, []CAPICluster]
	Kubeconfig  *cache.TTLCache[string, CachedKubeconfig]

	mu  sync.Mutex
	gen uint64 // bumped by Reset
}

// Generation identifies the config the cache contents belong to. Take it
// before fetching something to cache and store the result with IfGeneration,
// so a fetch that began before a Reset cannot put its (old config) result
// back into the emptied cache.
func (s *Store) Generation() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// IfGeneration runs store (which writes to the caches) only if no Reset
// has happened since gen was taken, and reports whether it ran. Reset waits
// for a running store, so a write is either before the reset (and cleared by
// it) or skipped.
func (s *Store) IfGeneration(gen uint64, store func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen {
		return false
	}
	store()
	return true
}

func NewStore(clusterListTTL, kubeconfigTTL time.Duration) *Store {
	return &Store{
		ClusterList: cache.New[string, []CAPICluster](clusterListTTL),
		Kubeconfig:  cache.New[string, CachedKubeconfig](kubeconfigTTL),
	}
}

// Reset empties both caches and sets the TTLs new entries get. A config
// reload does this: what is cached was derived from the old config (which
// kubeconfigs, which transforms), so none of it may outlive it.
func (s *Store) Reset(clusterListTTL, kubeconfigTTL time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	s.ClusterList.Clear()
	s.ClusterList.SetTTL(clusterListTTL)
	s.Kubeconfig.Clear()
	s.Kubeconfig.SetTTL(kubeconfigTTL)
}

func ClusterListKey(kubeconfigName, context string) string {
	return kubeconfigName + ":" + context
}

func KubeconfigKey(kubeconfigName, context, namespace, clusterName string) string {
	return kubeconfigName + ":" + context + ":" + namespace + ":" + clusterName
}
