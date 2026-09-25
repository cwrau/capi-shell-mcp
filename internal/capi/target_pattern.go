package capi

import (
	"fmt"
	"slices"
	"strings"
)

// TargetPattern is a possibly-partial target: 1 to 4 of
// kubeconfig/context/namespace/name, filled left-to-right. An empty field
// is a wildcard for that level (and, implicitly, everything below it,
// since a target string can't skip a level).
type TargetPattern struct {
	KubeconfigName string
	Context        string
	Namespace      string
	Name           string
}

// IsExact reports whether pattern fully identifies exactly one cluster
// (all four fields set) rather than a wildcard scope.
func (p TargetPattern) IsExact() bool {
	return p.Context != "" && p.Namespace != "" && p.Name != ""
}

// ParseTargetPattern parses target as 1 to 4 non-empty slash-separated
// segments: kubeconfig[/context[/namespace[/name]]].
func ParseTargetPattern(target string) (TargetPattern, error) {
	if target == "" {
		return TargetPattern{}, fmt.Errorf("capi: invalid target %q: must not be empty", target)
	}
	parts := strings.Split(target, "/")
	if len(parts) > 4 {
		return TargetPattern{}, fmt.Errorf("capi: invalid target %q: want 1 to 4 slash-separated segments (kubeconfig[/context[/namespace[/name]]])", target)
	}
	if slices.Contains(parts, "") {
		return TargetPattern{}, fmt.Errorf("capi: invalid target %q: empty segment", target)
	}

	var pattern TargetPattern
	pattern.KubeconfigName = parts[0]
	if len(parts) > 1 {
		pattern.Context = parts[1]
	}
	if len(parts) > 2 {
		pattern.Namespace = parts[2]
	}
	if len(parts) > 3 {
		pattern.Name = parts[3]
	}
	return pattern, nil
}

// Matches reports whether c satisfies every non-empty field of pattern.
func (p TargetPattern) Matches(c CAPICluster) bool {
	if c.KubeconfigName != p.KubeconfigName {
		return false
	}
	if p.Context != "" && c.Context != p.Context {
		return false
	}
	if p.Namespace != "" && c.Namespace != p.Namespace {
		return false
	}
	if p.Name != "" && c.Name != p.Name {
		return false
	}
	return true
}

// ExpandTargetPattern returns the composite target string (FormatTarget)
// of every cluster in all that matches pattern.
func ExpandTargetPattern(pattern TargetPattern, all []CAPICluster) []string {
	var out []string
	for _, c := range all {
		if pattern.Matches(c) {
			out = append(out, FormatTarget(c))
		}
	}
	return out
}
