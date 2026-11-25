// Package server builds the MCP server and its bespoke CAPI cluster
// tools (list_clusters, get_cluster_kubeconfig,
// exec_in_cluster(s)(_readonly)).
package server

import (
	"os"
	"sort"
	"sync/atomic"
	"time"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
	"github.com/cwrau/capi-shell-mcp/internal/config"
	"github.com/cwrau/capi-shell-mcp/internal/proxy"
	"github.com/cwrau/capi-shell-mcp/internal/shell"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	rbacv1client "k8s.io/client-go/kubernetes/typed/rbac/v1"
)

// ToolServer holds everything the bespoke tools need: config, the
// process-lifetime caches, the sshuttle proxy manager, and the seams for
// building Kubernetes clients / running commands.
type ToolServer struct {
	// cfg is swapped as a whole by Reload; read it through config().
	cfg               atomic.Pointer[config.AppConfig]
	store             *capi.Store
	proxyMgr          *proxy.Manager
	execer            shell.Execer
	clientsForContext ClientsForContextFunc
	// clientsFromReadonlySource builds Core/Rbac/Discovery clients directly from a
	// kubeconfig YAML string; overridable in tests. Production default is
	// ClientsFromKubeconfigYAML.
	clientsFromReadonlySource func(kubeconfigYAML string) (corev1client.CoreV1Interface, rbacv1client.RbacV1Interface, capi.APIResourceLister, error)
	// outputDirOverride replaces the default saved-output directory (under
	// XDG_RUNTIME_DIR); only tests set it.
	outputDirOverride string
}

func NewToolServer(cfg *config.AppConfig, store *capi.Store, proxyMgr *proxy.Manager, execer shell.Execer, clientsForContext ClientsForContextFunc) *ToolServer {
	s := &ToolServer{
		store:                     store,
		proxyMgr:                  proxyMgr,
		execer:                    execer,
		clientsForContext:         clientsForContext,
		clientsFromReadonlySource: ClientsFromKubeconfigYAML,
	}
	s.cfg.Store(cfg)
	return s
}

// config is the configuration in effect. A call that reads several settings
// should take it once, so a concurrent Reload cannot give it a mix.
func (s *ToolServer) config() *config.AppConfig { return s.cfg.Load() }

// snapshot is a configuration together with the cache generation it was
// read under. A call that looks things up in the config and caches what it
// fetches takes one at its start and uses it throughout: the generation is
// read first, so if a Reload lands in between, the config is the new one and
// the generation the old one, and the cache write is skipped (never the
// other way round, an old config cached under the new generation).
type snapshot struct {
	cfg *config.AppConfig
	gen uint64
}

func (s *ToolServer) snapshot() snapshot {
	gen := s.store.Generation()
	return snapshot{cfg: s.config(), gen: gen}
}

// Reload switches to cfg for every call that starts from now on; calls
// already running finish on the configuration they began with. The caches
// are emptied, since what they hold came from the old configuration.
func (s *ToolServer) Reload(cfg *config.AppConfig) {
	s.cfg.Store(cfg)
	s.store.Reset(
		time.Duration(cfg.Cache.ClusterListTTLSeconds)*time.Second,
		time.Duration(cfg.Cache.KubeconfigTTLSeconds)*time.Second,
	)
}

// resolvedCluster pairs a discovered CAPI cluster with the kubeconfig
// entry it was found through.
type resolvedCluster struct {
	Cluster capi.CAPICluster
	Source  config.KubeconfigSource
}

// sortedKubeconfigs returns the configured kubeconfig entries ordered by
// name, for deterministic iteration (Go maps have no order; the TS
// implementation relied on JS object insertion order instead).
func sortedKubeconfigs(cfg *config.AppConfig) []config.KubeconfigSource {
	kubeconfigs := cfg.Kubeconfigs
	names := make([]string, 0, len(kubeconfigs))
	for name := range kubeconfigs {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]config.KubeconfigSource, len(names))
	for i, name := range names {
		out[i] = kubeconfigs[name]
	}
	return out
}

func (s *ToolServer) resolveContexts(src config.KubeconfigSource) ([]string, error) {
	if len(src.Contexts) > 0 {
		return src.Contexts, nil
	}
	data, err := os.ReadFile(src.Path)
	if err != nil {
		return nil, err
	}
	return capi.GetContexts(data)
}
