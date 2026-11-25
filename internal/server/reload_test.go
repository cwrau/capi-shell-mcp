package server

import (
	"context"
	"testing"
	"time"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
	"github.com/cwrau/capi-shell-mcp/internal/config"
)

func TestReloadSwitchesConfigAndEmptiesTheCaches(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	s.store.ClusterList.Set("prod:ctx-a", []capi.CAPICluster{{Name: "cluster-1"}})
	s.store.Kubeconfig.Set("k", capi.CachedKubeconfig{Kubeconfig: "old"})

	s.Reload(&config.AppConfig{
		Kubeconfigs: map[string]config.KubeconfigSource{"other": {Name: "other"}},
		Cache:       config.CacheConfig{ClusterListTTLSeconds: 7, KubeconfigTTLSeconds: 9},
		ReadOnly:    config.ReadOnlyConfig{DeniedResources: []string{}},
	})

	if _, ok := s.config().Kubeconfigs["other"]; !ok || len(s.config().Kubeconfigs) != 1 {
		t.Errorf("kubeconfigs = %v, want only the reloaded one", s.config().Kubeconfigs)
	}
	if got := s.kubeconfigTTL(); got != 9*time.Second {
		t.Errorf("kubeconfigTTL = %v, want 9s", got)
	}
	if _, ok := s.store.ClusterList.Get("prod:ctx-a"); ok {
		t.Error("cluster list cache survived the reload")
	}
	if _, ok := s.store.Kubeconfig.Get("k"); ok {
		t.Error("kubeconfig cache survived the reload")
	}
	// A new entry gets the reloaded TTL.
	s.store.ClusterList.Set("x", nil)
	if _, ok := s.store.ClusterList.Get("x"); !ok {
		t.Error("entry set after the reload is not readable")
	}
}

func TestFetchThatStraddlesAReloadIsNotCached(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	snap := s.snapshot()
	src := snap.cfg.Kubeconfigs["prod"]
	inner := s.clientsForContext
	s.clientsForContext = func(src config.KubeconfigSource, ctxName string) (*Clients, error) {
		// The config is reloaded while this call is fetching with the old one.
		s.Reload(s.config())
		return inner(src, ctxName)
	}

	if _, err := s.listClustersCached(context.Background(), snap, src, "ctx-a"); err != nil {
		t.Fatalf("listClustersCached: %v", err)
	}
	if _, ok := s.store.ClusterList.Get(capi.ClusterListKey("prod", "ctx-a")); ok {
		t.Error("cluster list fetched under the old config was cached after the reload")
	}
}
