package capi

import "testing"

func TestParseTargetPatternHandlesOneToFourSegments(t *testing.T) {
	cases := []struct {
		target string
		want   TargetPattern
	}{
		{"prod", TargetPattern{KubeconfigName: "prod"}},
		{"prod/ctx-a", TargetPattern{KubeconfigName: "prod", Context: "ctx-a"}},
		{"prod/ctx-a/ns-1", TargetPattern{KubeconfigName: "prod", Context: "ctx-a", Namespace: "ns-1"}},
		{"prod/ctx-a/ns-1/cluster-1", TargetPattern{KubeconfigName: "prod", Context: "ctx-a", Namespace: "ns-1", Name: "cluster-1"}},
	}
	for _, tc := range cases {
		got, err := ParseTargetPattern(tc.target)
		if err != nil {
			t.Fatalf("ParseTargetPattern(%q): %v", tc.target, err)
		}
		if got != tc.want {
			t.Errorf("ParseTargetPattern(%q) = %+v, want %+v", tc.target, got, tc.want)
		}
	}
}

func TestParseTargetPatternErrorsOnEmptyOrTooManySegments(t *testing.T) {
	for _, bad := range []string{"", "prod//ns-1", "prod/ctx-a/ns-1/cluster-1/extra"} {
		if _, err := ParseTargetPattern(bad); err == nil {
			t.Errorf("ParseTargetPattern(%q): expected error, got nil", bad)
		}
	}
}

func TestTargetPatternIsExact(t *testing.T) {
	exact := TargetPattern{KubeconfigName: "prod", Context: "ctx-a", Namespace: "ns-1", Name: "cluster-1"}
	if !exact.IsExact() {
		t.Error("IsExact() = false for a fully-specified pattern, want true")
	}
	partial := TargetPattern{KubeconfigName: "prod", Context: "ctx-a"}
	if partial.IsExact() {
		t.Error("IsExact() = true for a partial pattern, want false")
	}
}

func TestExpandTargetPatternMatchesOnKubeconfigOnly(t *testing.T) {
	all := []CAPICluster{
		{KubeconfigName: "prod", Context: "ctx-a", Namespace: "ns-1", Name: "c1"},
		{KubeconfigName: "prod", Context: "ctx-b", Namespace: "ns-2", Name: "c2"},
		{KubeconfigName: "dev", Context: "ctx-a", Namespace: "ns-1", Name: "c1"},
	}
	pattern, err := ParseTargetPattern("prod")
	if err != nil {
		t.Fatalf("ParseTargetPattern: %v", err)
	}
	got := ExpandTargetPattern(pattern, all)
	want := []string{"prod/ctx-a/ns-1/c1", "prod/ctx-b/ns-2/c2"}
	if !sameSet(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExpandTargetPatternMatchesOnKubeconfigAndContext(t *testing.T) {
	all := []CAPICluster{
		{KubeconfigName: "prod", Context: "ctx-a", Namespace: "ns-1", Name: "c1"},
		{KubeconfigName: "prod", Context: "ctx-b", Namespace: "ns-2", Name: "c2"},
	}
	pattern, err := ParseTargetPattern("prod/ctx-a")
	if err != nil {
		t.Fatalf("ParseTargetPattern: %v", err)
	}
	got := ExpandTargetPattern(pattern, all)
	want := []string{"prod/ctx-a/ns-1/c1"}
	if !sameSet(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExpandTargetPatternMatchesOnKubeconfigContextAndNamespace(t *testing.T) {
	all := []CAPICluster{
		{KubeconfigName: "prod", Context: "ctx-a", Namespace: "ns-1", Name: "c1"},
		{KubeconfigName: "prod", Context: "ctx-a", Namespace: "ns-1", Name: "c2"},
		{KubeconfigName: "prod", Context: "ctx-a", Namespace: "ns-2", Name: "c3"},
	}
	pattern, err := ParseTargetPattern("prod/ctx-a/ns-1")
	if err != nil {
		t.Fatalf("ParseTargetPattern: %v", err)
	}
	got := ExpandTargetPattern(pattern, all)
	want := []string{"prod/ctx-a/ns-1/c1", "prod/ctx-a/ns-1/c2"}
	if !sameSet(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExpandTargetPatternExactMatchesExactlyOne(t *testing.T) {
	all := []CAPICluster{
		{KubeconfigName: "prod", Context: "ctx-a", Namespace: "ns-1", Name: "c1"},
		{KubeconfigName: "prod", Context: "ctx-a", Namespace: "ns-1", Name: "c2"},
	}
	pattern, err := ParseTargetPattern("prod/ctx-a/ns-1/c1")
	if err != nil {
		t.Fatalf("ParseTargetPattern: %v", err)
	}
	got := ExpandTargetPattern(pattern, all)
	want := []string{"prod/ctx-a/ns-1/c1"}
	if !sameSet(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExpandTargetPatternReturnsEmptyWhenNothingMatches(t *testing.T) {
	all := []CAPICluster{{KubeconfigName: "prod", Context: "ctx-a", Namespace: "ns-1", Name: "c1"}}
	pattern, err := ParseTargetPattern("nope")
	if err != nil {
		t.Fatalf("ParseTargetPattern: %v", err)
	}
	got := ExpandTargetPattern(pattern, all)
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	set := make(map[string]bool, len(want))
	for _, w := range want {
		set[w] = true
	}
	for _, g := range got {
		if !set[g] {
			return false
		}
	}
	return true
}
