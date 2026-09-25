package server

// ClusterFilterInput selects zero or more clusters by composite target
// pattern (see capi.ParseTargetPattern) — used by tools that can't mutate
// workload state, so a partial pattern is safe to expand and an omitted
// filter safely means "every known cluster".
type ClusterFilterInput struct {
	Clusters []string `json:"clusters,omitempty" jsonschema:"One or more cluster filters, each \"kubeconfig[/context[/namespace[/name]]]\" (from a prior list_clusters call's target field, or a shorter prefix to match every cluster under it). Omit entirely to select every known cluster; an empty array selects none."`
}

// ExecInput is the input for exec_in_cluster(_readonly): one or more
// clusters (each a full, exact target — no partial/prefix matching, since
// the command being run is opaque to us) plus a command to run in each.
type ExecInput struct {
	Clusters []string `json:"clusters" jsonschema:"One or more cluster targets, each the full \"kubeconfig/context/namespace/name\" path (from a prior list_clusters call's target field) — no partial/prefix matching for this tool. Required; an empty array runs against none."`
	Command  []string `json:"command" jsonschema:"Command + args to run, e.g. [\"kubectl\",\"get\",\"nodes\"]."`
	Stdin    string   `json:"stdin,omitempty" jsonschema:"Data to pipe to the command's stdin, e.g. YAML for \"kubectl apply -f -\"."`
}
