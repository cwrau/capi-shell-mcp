package capi

import "fmt"

// ResolveClusterPatterns turns patterns (each a 1-to-4-segment
// kubeconfig[/context[/namespace[/name]]] target pattern) into
// deduplicated exact composite target strings (first-seen order),
// shared by every tool that accepts a "clusters" argument — our own
// bespoke tools and the embedded kubernetes-mcp-server tools alike.
//
// If strict is true, every pattern must already be an exact 4-segment
// path — used for tools that can't safely be classified as read-only
// (e.g. one that runs an arbitrary, opaque command), so a partial
// pattern never silently expands into an unintended blast radius.
// listKnown is never called in that case.
//
// If strict is false, a non-exact pattern is expanded (prefix match)
// against every cluster listKnown returns. listKnown is called at most
// once, lazily, only if at least one pattern actually needs expanding —
// an already-exact pattern never triggers it, since that lookup can be a
// real, possibly-expensive re-list across every configured kubeconfig.
func ResolveClusterPatterns(patterns []string, listKnown func() ([]CAPICluster, error), strict bool) ([]string, error) {
	if strict {
		for _, p := range patterns {
			if _, _, _, _, err := ParseTarget(p); err != nil {
				return nil, fmt.Errorf("cluster entry %q must be a full kubeconfig/context/namespace/name path: %w", p, err)
			}
		}
		return dedupePreserveOrder(patterns), nil
	}

	parsed := make([]TargetPattern, len(patterns))
	needsExpansion := false
	for i, p := range patterns {
		pattern, err := ParseTargetPattern(p)
		if err != nil {
			return nil, err
		}
		parsed[i] = pattern
		if !pattern.IsExact() {
			needsExpansion = true
		}
	}

	var known []CAPICluster
	if needsExpansion {
		k, err := listKnown()
		if err != nil {
			return nil, err
		}
		known = k
	}

	seen := make(map[string]bool)
	var out []string
	for _, pattern := range parsed {
		var matches []string
		if pattern.IsExact() {
			matches = []string{FormatTarget(CAPICluster{
				KubeconfigName: pattern.KubeconfigName,
				Context:        pattern.Context,
				Namespace:      pattern.Namespace,
				Name:           pattern.Name,
			})}
		} else {
			matches = ExpandTargetPattern(pattern, known)
		}
		for _, t := range matches {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out, nil
}

func dedupePreserveOrder(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
