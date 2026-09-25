package capi

import (
	"testing"
	"time"
)

func TestClusterListKey(t *testing.T) {
	if got := ClusterListKey("prod", "prod-admin"); got != "prod:prod-admin" {
		t.Errorf("ClusterListKey = %q", got)
	}
}

func TestKubeconfigKey(t *testing.T) {
	got := KubeconfigKey("prod", "prod-admin", "default", "my-cluster")
	if got != "prod:prod-admin:default:my-cluster" {
		t.Errorf("KubeconfigKey = %q", got)
	}
}

func TestStoreKubeconfigCacheRoundTripsWithAndWithoutProxyTarget(t *testing.T) {
	store := NewStore(60*time.Second, 60*time.Second)

	store.Kubeconfig.Set("with-proxy", CachedKubeconfig{
		Kubeconfig:  "yaml-content",
		ProxyTarget: &ProxyTarget{SshuttleHost: "gw", APIServerIP: "10.0.0.1", APIServerPort: "6443"},
	})
	got, ok := store.Kubeconfig.Get("with-proxy")
	if !ok || got.Kubeconfig != "yaml-content" || got.ProxyTarget == nil || got.ProxyTarget.SshuttleHost != "gw" {
		t.Fatalf("got %+v, ok=%v", got, ok)
	}

	store.Kubeconfig.Set("no-proxy", CachedKubeconfig{Kubeconfig: "yaml-content"})
	got2, ok := store.Kubeconfig.Get("no-proxy")
	if !ok || got2.Kubeconfig != "yaml-content" || got2.ProxyTarget != nil {
		t.Fatalf("got %+v, ok=%v", got2, ok)
	}
}
