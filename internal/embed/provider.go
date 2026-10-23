// Package embed mounts kubernetes-mcp-server's tools onto our own MCP
// server, wrapped with our CAPI multi-cluster selection logic: every
// mounted tool's "cluster" parameter is one of our composite target
// strings (capi.FormatTarget), resolved to a real per-cluster client via
// capiProvider.
package embed

import (
	"context"
	"errors"
	"fmt"
	"sync"

	kmcpconfig "github.com/containers/kubernetes-mcp-server/pkg/config"
	kmcpkube "github.com/containers/kubernetes-mcp-server/pkg/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// errUnknownTarget is kubernetes-mcp-server's sentinel for "this target
// doesn't exist" — GetDerivedKubernetes must return an error that wraps
// it, or their tool handler treats a lookup failure as a generic
// protocol-level error instead of a clean per-call tool result.
var errUnknownTarget = kmcpkube.ErrUnknownTarget

// TargetResolver is the seam our CAPI cluster-selection logic plugs in
// through: ResolveTarget turns one of our composite target strings into
// the admin kubeconfig YAML for that workload cluster (fetched/cached/
// tunneled exactly like our own tools), and ListTargets enumerates every
// currently-known target string.
type TargetResolver interface {
	ResolveTarget(ctx context.Context, target string) (kubeconfigYAML string, err error)
	ListTargets(ctx context.Context) ([]string, error)
}

// targetParameterName is the argument name every mounted tool's "which
// cluster" parameter gets, via the WithTargetParameter mutator.
const targetParameterName = "cluster"

// capiProvider implements kubernetes.Provider by routing every target
// lookup through our own CAPI cluster-discovery/kubeconfig-cache/sshuttle
// pipeline (via resolver) instead of a single static kubeconfig.
type capiProvider struct {
	resolver TargetResolver

	mu       sync.Mutex
	cfg      *kmcpconfig.Config
	managers map[string]*kmcpkube.Manager
}

var _ kmcpkube.Provider = (*capiProvider)(nil)

func newCapiProvider(resolver TargetResolver, cfg *kmcpconfig.Config) *capiProvider {
	return &capiProvider{resolver: resolver, cfg: cfg, managers: make(map[string]*kmcpkube.Manager)}
}

// --- api.TargetProvider ---

func (p *capiProvider) IsMultiTarget() bool { return true }

func (p *capiProvider) GetTargets(ctx context.Context) ([]string, error) {
	return p.resolver.ListTargets(ctx)
}

func (p *capiProvider) GetDefaultTarget() string { return "" }

func (p *capiProvider) GetTargetParameterName() string { return targetParameterName }

// --- kubernetes.Provider ---

func (p *capiProvider) GetDerivedKubernetes(ctx context.Context, target string) (*kmcpkube.Kubernetes, error) {
	mgr, err := p.managerForTarget(ctx, target)
	if err != nil {
		return nil, err
	}
	return mgr.Derived(ctx)
}

func (p *capiProvider) managerForTarget(ctx context.Context, target string) (*kmcpkube.Manager, error) {
	p.mu.Lock()
	mgr, ok := p.managers[target]
	p.mu.Unlock()
	if ok {
		return mgr, nil
	}

	kubeconfigYAML, err := p.resolver.ResolveTarget(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errUnknownTarget, target, err)
	}

	rawConfig, err := clientcmd.Load([]byte(kubeconfigYAML))
	if err != nil {
		return nil, fmt.Errorf("embed: parsing kubeconfig for target %s: %w", target, err)
	}
	clientCmdConfig := clientcmd.NewNonInteractiveClientConfig(*rawConfig, "", &clientcmd.ConfigOverrides{}, nil)
	restConfig, err := clientCmdConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("embed: building rest config for target %s: %w", target, err)
	}

	p.mu.Lock()
	cfg := p.cfg
	p.mu.Unlock()
	newMgr, err := kmcpkube.NewManager(ctx, cfg, restConfig, clientCmdConfig)
	if err != nil {
		return nil, fmt.Errorf("embed: building manager for target %s: %w", target, err)
	}

	p.mu.Lock()
	p.managers[target] = newMgr
	p.mu.Unlock()
	return newMgr, nil
}

func (p *capiProvider) WatchTargets(context.Context, kmcpkube.McpReloader) {
	// No live-watch: cluster-list changes are already picked up per-call
	// via our own TTL cache, and config changes are handled by restarting
	// the whole daemon, not a hot reload.
}

func (p *capiProvider) ReloadConfig(_ context.Context, cfg *kmcpconfig.Config) error {
	if cfg == nil {
		return errors.New("embed: config cannot be nil")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg = cfg
	return nil
}

func (p *capiProvider) PublishKubernetesConfig(cfg *kmcpconfig.Config) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, mgr := range p.managers {
		mgr.SetConfig(cfg)
	}
}

func (p *capiProvider) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, mgr := range p.managers {
		mgr.Close()
	}
}
