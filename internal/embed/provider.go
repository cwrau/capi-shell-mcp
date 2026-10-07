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
	"time"

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
// tunneled exactly like our own tools), ResolveTargetReadonly returns a
// freshly built, impersonating read-only kubeconfig for the same cluster (valid for
// ReadonlyTTL), and ListTargets enumerates every currently-known target
// string.
type TargetResolver interface {
	ResolveTarget(ctx context.Context, target string) (kubeconfigYAML string, err error)
	ResolveTargetReadonly(ctx context.Context, target string) (kubeconfigYAML string, err error)
	ReadonlyTTL() time.Duration
	ListTargets(ctx context.Context) ([]string, error)
}

// targetParameterName is the argument name every mounted tool's "which
// cluster" parameter gets, via the WithTargetParameter mutator.
const targetParameterName = "cluster"

// capiProvider implements kubernetes.Provider by routing every target
// lookup through our own CAPI cluster-discovery/kubeconfig-cache/sshuttle
// pipeline (via resolver) instead of a single static kubeconfig.
//
// A readonly provider hands out clients built from the impersonating read-only
// kubeconfig instead of the admin one, so every tool served by it is bound
// by the read-only RBAC regardless of what the tool itself tries to do.
// Cached managers expire after half of ReadonlyTTL, which also regenerates
// the read-only role regularly.
type capiProvider struct {
	resolver TargetResolver
	readonly bool

	mu       sync.Mutex
	cfg      *kmcpconfig.Config
	managers map[string]managerEntry
}

type managerEntry struct {
	mgr     *kmcpkube.Manager
	expires time.Time // zero = never
}

var _ kmcpkube.Provider = (*capiProvider)(nil)

func newCapiProvider(resolver TargetResolver, cfg *kmcpconfig.Config, readonly bool) *capiProvider {
	return &capiProvider{resolver: resolver, cfg: cfg, readonly: readonly, managers: make(map[string]managerEntry)}
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
	entry, ok := p.managers[target]
	p.mu.Unlock()
	if ok && (entry.expires.IsZero() || time.Now().Before(entry.expires)) {
		return entry.mgr, nil
	}

	resolve := p.resolver.ResolveTarget
	if p.readonly {
		resolve = p.resolver.ResolveTargetReadonly
	}
	kubeconfigYAML, err := resolve(ctx, target)
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

	fresh := managerEntry{mgr: newMgr}
	if p.readonly {
		if ttl := p.resolver.ReadonlyTTL(); ttl > 0 {
			fresh.expires = time.Now().Add(ttl / 2)
		}
	}
	p.mu.Lock()
	p.managers[target] = fresh
	p.mu.Unlock()
	if ok {
		// The replaced manager's token outlives the grace period, so calls
		// already running on it finish before it is closed.
		time.AfterFunc(p.resolver.ReadonlyTTL()/4, entry.mgr.Close)
	}
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
	for _, e := range p.managers {
		e.mgr.SetConfig(cfg)
	}
}

func (p *capiProvider) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.managers {
		e.mgr.Close()
	}
}
