package capi

import (
	"errors"
	"testing"
)

func TestResolveClusterPatternsStrictPassesExactPathsThroughAndDedups(t *testing.T) {
	got, err := ResolveClusterPatterns(
		[]string{"prod/ctx-a/ns-1/c1", "prod/ctx-a/ns-1/c1", "prod/ctx-a/ns-1/c2"},
		func() ([]CAPICluster, error) { t.Fatal("listKnown must not be called in strict mode"); return nil, nil },
		true,
	)
	if err != nil {
		t.Fatalf("ResolveClusterPatterns: %v", err)
	}
	want := []string{"prod/ctx-a/ns-1/c1", "prod/ctx-a/ns-1/c2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestResolveClusterPatternsStrictRejectsPartialPattern(t *testing.T) {
	_, err := ResolveClusterPatterns(
		[]string{"prod/ctx-a"},
		func() ([]CAPICluster, error) { return nil, nil },
		true,
	)
	if err == nil {
		t.Fatal("expected error for a partial pattern in strict mode")
	}
}

func TestResolveClusterPatternsLenientExpandsPartialPattern(t *testing.T) {
	known := []CAPICluster{
		{KubeconfigName: "prod", Context: "ctx-a", Namespace: "ns-1", Name: "c1"},
		{KubeconfigName: "prod", Context: "ctx-a", Namespace: "ns-2", Name: "c2"},
	}
	got, err := ResolveClusterPatterns(
		[]string{"prod/ctx-a/ns-1"},
		func() ([]CAPICluster, error) { return known, nil },
		false,
	)
	if err != nil {
		t.Fatalf("ResolveClusterPatterns: %v", err)
	}
	if len(got) != 1 || got[0] != "prod/ctx-a/ns-1/c1" {
		t.Errorf("got %v, want [prod/ctx-a/ns-1/c1]", got)
	}
}

func TestResolveClusterPatternsLenientSkipsListKnownWhenAllExact(t *testing.T) {
	got, err := ResolveClusterPatterns(
		[]string{"prod/ctx-a/ns-1/c1"},
		func() ([]CAPICluster, error) {
			return nil, errors.New("listKnown must not be called for an already-exact pattern")
		},
		false,
	)
	if err != nil {
		t.Fatalf("ResolveClusterPatterns: %v", err)
	}
	if len(got) != 1 || got[0] != "prod/ctx-a/ns-1/c1" {
		t.Errorf("got %v, want [prod/ctx-a/ns-1/c1]", got)
	}
}

func TestResolveClusterPatternsLenientDedupsAcrossOverlappingPatterns(t *testing.T) {
	known := []CAPICluster{
		{KubeconfigName: "prod", Context: "ctx-a", Namespace: "ns-1", Name: "c1"},
	}
	got, err := ResolveClusterPatterns(
		[]string{"prod", "prod/ctx-a", "prod/ctx-a/ns-1/c1"},
		func() ([]CAPICluster, error) { return known, nil },
		false,
	)
	if err != nil {
		t.Fatalf("ResolveClusterPatterns: %v", err)
	}
	if len(got) != 1 || got[0] != "prod/ctx-a/ns-1/c1" {
		t.Errorf("got %v, want a single deduplicated entry [prod/ctx-a/ns-1/c1]", got)
	}
}

func TestResolveClusterPatternsLenientPropagatesListKnownError(t *testing.T) {
	_, err := ResolveClusterPatterns(
		[]string{"prod"},
		func() ([]CAPICluster, error) { return nil, errors.New("boom") },
		false,
	)
	if err == nil {
		t.Fatal("expected error to propagate from listKnown")
	}
}
