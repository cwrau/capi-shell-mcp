package server

import (
	"context"
	"strings"
	"testing"
)

func TestListTargetsReturnsCompositeTargetStrings(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1", "cluster-2")
	got, err := s.ListTargets(context.Background())
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	want := map[string]bool{"prod/ctx-a/ns-1/cluster-1": true, "prod/ctx-a/ns-1/cluster-2": true}
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 entries", got)
	}
	for _, target := range got {
		if !want[target] {
			t.Errorf("unexpected target %q", target)
		}
	}
}

func TestResolveTargetReturnsKubeconfigForTheNamedCluster(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	kc, err := s.ResolveTarget(context.Background(), "prod/ctx-a/ns-1/cluster-1")
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if !strings.Contains(kc, "kind: Config") {
		t.Fatalf("kubeconfig = %q, want it to look like a kubeconfig", kc)
	}
}

func TestResolveTargetErrorsOnUnknownKubeconfig(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	_, err := s.ResolveTarget(context.Background(), "nope/ctx-a/ns-1/cluster-1")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want it to mention the unknown kubeconfig", err)
	}
}

func TestResolveTargetErrorsOnMalformedTarget(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	_, err := s.ResolveTarget(context.Background(), "not-a-valid-target")
	if err == nil {
		t.Fatal("expected error for malformed target")
	}
}
