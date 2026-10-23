package embed

import (
	"context"
	"errors"
	"sync"
	"testing"

	kmcpconfig "github.com/containers/kubernetes-mcp-server/pkg/config"
)

const fakeKubeconfig = `
apiVersion: v1
kind: Config
clusters:
- name: workload
  cluster:
    server: https://10.0.0.1:6443
contexts:
- name: workload
  context:
    cluster: workload
    user: admin
current-context: workload
users:
- name: admin
  user:
    token: sometoken
`

type fakeResolver struct {
	mu             sync.Mutex
	resolveCalls   int
	listTargetsErr error
	listCalls      int
	targets        []string
	kubeconfigByID map[string]string
	err            error
}

func (r *fakeResolver) ListTargets(context.Context) ([]string, error) {
	r.mu.Lock()
	r.listCalls++
	r.mu.Unlock()
	if r.listTargetsErr != nil {
		return nil, r.listTargetsErr
	}
	return r.targets, nil
}

func (r *fakeResolver) listTargetsCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listCalls
}

func (r *fakeResolver) ResolveTarget(_ context.Context, target string) (string, error) {
	r.mu.Lock()
	r.resolveCalls++
	r.mu.Unlock()
	if r.err != nil {
		return "", r.err
	}
	kc, ok := r.kubeconfigByID[target]
	if !ok {
		return "", errors.New("no such target in fixture")
	}
	return kc, nil
}

func (r *fakeResolver) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resolveCalls
}

func TestCapiProviderIsAlwaysMultiTarget(t *testing.T) {
	p := newCapiProvider(&fakeResolver{}, kmcpconfig.New())
	if !p.IsMultiTarget() {
		t.Error("IsMultiTarget() = false, want true")
	}
}

func TestCapiProviderTargetParameterNameIsCluster(t *testing.T) {
	p := newCapiProvider(&fakeResolver{}, kmcpconfig.New())
	if p.GetTargetParameterName() != "cluster" {
		t.Errorf("GetTargetParameterName() = %q, want %q", p.GetTargetParameterName(), "cluster")
	}
}

func TestCapiProviderGetTargetsDelegatesToResolver(t *testing.T) {
	resolver := &fakeResolver{targets: []string{"prod/ctx-a/ns/c1", "prod/ctx-a/ns/c2"}}
	p := newCapiProvider(resolver, kmcpconfig.New())

	got, err := p.GetTargets(context.Background())
	if err != nil {
		t.Fatalf("GetTargets: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 targets", got)
	}
}

func TestCapiProviderReloadConfigRejectsNil(t *testing.T) {
	p := newCapiProvider(&fakeResolver{}, kmcpconfig.New())
	if err := p.ReloadConfig(context.Background(), nil); err == nil {
		t.Error("ReloadConfig(nil) = nil, want error")
	}
}

func TestCapiProviderReloadedConfigIsUsedForNewManagersAndPublishedToExisting(t *testing.T) {
	resolver := &fakeResolver{kubeconfigByID: map[string]string{
		"prod/ctx-a/ns/c1": fakeKubeconfig,
		"prod/ctx-a/ns/c2": fakeKubeconfig,
	}}
	p := newCapiProvider(resolver, kmcpconfig.New())
	defer p.Close()

	if _, err := p.GetDerivedKubernetes(context.Background(), "prod/ctx-a/ns/c1"); err != nil {
		t.Fatalf("GetDerivedKubernetes c1: %v", err)
	}

	reloaded := kmcpconfig.New()
	if err := p.ReloadConfig(context.Background(), reloaded); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	if _, err := p.GetDerivedKubernetes(context.Background(), "prod/ctx-a/ns/c2"); err != nil {
		t.Fatalf("GetDerivedKubernetes c2: %v", err)
	}
	if got := p.managers["prod/ctx-a/ns/c2"].Config(); got != reloaded {
		t.Error("manager built after ReloadConfig does not use the reloaded config")
	}
	if got := p.managers["prod/ctx-a/ns/c1"].Config(); got == reloaded {
		t.Error("existing manager picked up the reloaded config before PublishKubernetesConfig")
	}

	p.PublishKubernetesConfig(reloaded)
	if got := p.managers["prod/ctx-a/ns/c1"].Config(); got != reloaded {
		t.Error("existing manager did not pick up the config after PublishKubernetesConfig")
	}
}

func TestCapiProviderGetDerivedKubernetesBuildsAClientForAKnownTarget(t *testing.T) {
	resolver := &fakeResolver{kubeconfigByID: map[string]string{"prod/ctx-a/ns/c1": fakeKubeconfig}}
	p := newCapiProvider(resolver, kmcpconfig.New())
	defer p.Close()

	k, err := p.GetDerivedKubernetes(context.Background(), "prod/ctx-a/ns/c1")
	if err != nil {
		t.Fatalf("GetDerivedKubernetes: %v", err)
	}
	if k == nil {
		t.Fatal("GetDerivedKubernetes returned a nil client")
	}
}

func TestCapiProviderGetDerivedKubernetesCachesManagerPerTarget(t *testing.T) {
	resolver := &fakeResolver{kubeconfigByID: map[string]string{"prod/ctx-a/ns/c1": fakeKubeconfig}}
	p := newCapiProvider(resolver, kmcpconfig.New())
	defer p.Close()

	if _, err := p.GetDerivedKubernetes(context.Background(), "prod/ctx-a/ns/c1"); err != nil {
		t.Fatalf("first GetDerivedKubernetes: %v", err)
	}
	if _, err := p.GetDerivedKubernetes(context.Background(), "prod/ctx-a/ns/c1"); err != nil {
		t.Fatalf("second GetDerivedKubernetes: %v", err)
	}
	if resolver.calls() != 1 {
		t.Errorf("resolver.ResolveTarget called %d times, want 1 (second call should reuse the cached manager)", resolver.calls())
	}
}

func TestCapiProviderGetDerivedKubernetesWrapsResolverErrorAsUnknownTarget(t *testing.T) {
	resolver := &fakeResolver{err: errors.New("boom")}
	p := newCapiProvider(resolver, kmcpconfig.New())
	defer p.Close()

	_, err := p.GetDerivedKubernetes(context.Background(), "nope/ctx/ns/c1")
	if err == nil {
		t.Fatal("expected an error for a resolver failure")
	}
	if !errors.Is(err, errUnknownTarget) {
		t.Errorf("err = %v, want it to wrap errUnknownTarget", err)
	}
}
