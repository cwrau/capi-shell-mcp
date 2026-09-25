// Package server builds the MCP server and its 7 bespoke CAPI cluster
// tools (list_clusters, get_cluster_kubeconfig(_readonly),
// exec_in_cluster(s)(_readonly)).
package server

import (
	"os"
	"sort"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
	"github.com/cwrau/capi-shell-mcp/internal/config"
	"github.com/cwrau/capi-shell-mcp/internal/proxy"
	"github.com/cwrau/capi-shell-mcp/internal/shell"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	rbacv1client "k8s.io/client-go/kubernetes/typed/rbac/v1"
)

// ToolServer holds everything the 7 bespoke tools need: config, the
// process-lifetime caches, the sshuttle proxy manager, and the seams for
// building Kubernetes clients / running commands.
type ToolServer struct {
	cfg               *config.AppConfig
	store             *capi.Store
	proxyMgr          *proxy.Manager
	execer            shell.Execer
	clientsForContext ClientsForContextFunc
	// clientsFromReadonlySource builds Core/Rbac clients directly from a
	// kubeconfig YAML string; overridable in tests. Production default is
	// ClientsFromKubeconfigYAML.
	clientsFromReadonlySource func(kubeconfigYAML string) (corev1client.CoreV1Interface, rbacv1client.RbacV1Interface, error)
}

func NewToolServer(cfg *config.AppConfig, store *capi.Store, proxyMgr *proxy.Manager, execer shell.Execer, clientsForContext ClientsForContextFunc) *ToolServer {
	return &ToolServer{
		cfg:                       cfg,
		store:                     store,
		proxyMgr:                  proxyMgr,
		execer:                    execer,
		clientsForContext:         clientsForContext,
		clientsFromReadonlySource: ClientsFromKubeconfigYAML,
	}
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
func (s *ToolServer) sortedKubeconfigs() []config.KubeconfigSource {
	names := make([]string, 0, len(s.cfg.Kubeconfigs))
	for name := range s.cfg.Kubeconfigs {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]config.KubeconfigSource, len(names))
	for i, name := range names {
		out[i] = s.cfg.Kubeconfigs[name]
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
