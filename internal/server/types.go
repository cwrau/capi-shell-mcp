package server

// ClusterFilterInput selects zero or more clusters by composite target
// pattern (see capi.ParseTargetPattern) — used by tools that can't mutate
// workload state, so a partial pattern is safe to expand and an omitted
// filter safely means "every known cluster".
type ClusterFilterInput struct {
	Clusters []string `json:"clusters,omitempty" jsonschema:"One or more cluster filters, each \"kubeconfig[/context[/namespace[/name]]]\" (from a prior list_clusters call's target field, or a shorter prefix to match every cluster under it). Omit entirely to select every known cluster; an empty array selects none."`
}

// ExecInput is the input for exec_in_cluster: one or more clusters (each a
// full, exact target — no partial/prefix matching, since the command being
// run is arbitrary) plus a command to run in each.
type ExecInput struct {
	Clusters     []string `json:"clusters" jsonschema:"One or more cluster targets, each the full \"kubeconfig/context/namespace/name\" path (from a prior list_clusters call's target field) — no partial/prefix matching for this tool. Required; an empty array runs against none."`
	Command      []string `json:"command" jsonschema:"Command + args to run, e.g. [\"kubectl\",\"get\",\"nodes\"]."`
	Stdin        string   `json:"stdin,omitempty" jsonschema:"Data to pipe to the command's stdin, e.g. YAML for \"kubectl apply -f -\"."`
	IgnoreErrors bool     `json:"ignore_errors,omitempty" jsonschema:"If true, a cluster where the command fails (non-zero exit, or the cluster can't be reached) is still listed in the result with its exit_code, stderr and error, but the tool call is not marked as an error. Use it when failures on some clusters are expected, e.g. a resource kind only some clusters have. A rejected call (bad arguments, bad jq) is still an error."`
}

// ReadonlyExecInput is the input for exec_in_cluster_readonly. It can only
// run kubectl with allowlisted read-only arguments, so unlike ExecInput it
// accepts partial cluster patterns and an omitted "clusters" (= all).
type ReadonlyExecInput struct {
	Clusters []string `json:"clusters,omitempty" jsonschema:"One or more cluster filters, each \"kubeconfig[/context[/namespace[/name]]]\" (from a prior list_clusters call's target field, or a shorter prefix to match every cluster under it). Omit entirely to run against every known cluster; an empty array runs against none."`
	Args     []string `json:"args" jsonschema:"kubectl arguments — everything after \"kubectl\" — e.g. [\"get\",\"pods\",\"-n\",\"kube-system\",\"-o\",\"json\"]. Only kubectl runs. The first element must be a read verb (get, describe, logs, top, events, api-resources, api-versions, explain, version, auth can-i|whoami) and only a fixed set of flags per verb is accepted; anything else is rejected with an explanation."`
	JQ       string   `json:"jq,omitempty" jsonschema:"Optional jq expression applied in-process to each cluster's stdout, which must then be JSON (use -o json). String results print raw (like jq -r), everything else as indented JSON. Only applied when kubectl exits 0. The expression gets 30 seconds per cluster and its output is capped at 10 MiB; kubectl output up to 32 MiB per cluster is accepted when jq is used (10 MiB otherwise)."`
	// IgnoreErrors: see ExecInput.
	IgnoreErrors bool `json:"ignore_errors,omitempty" jsonschema:"If true, a cluster where kubectl fails (non-zero exit, or the cluster can't be reached) is still listed in the result with its exit_code, stderr and error, but the tool call is not marked as an error. Use it when failures on some clusters are expected, e.g. listing a resource kind only some clusters have. A rejected call (bad arguments, bad jq) is still an error."`
	SaveOutput   bool `json:"save_output,omitempty" jsonschema:"If true, each cluster's stdout (after jq) is written to a private 0600 file on the daemon host instead of being returned inline; the result carries stdout_file and stdout_bytes. Files are removed after about an hour. Use for large outputs: kubectl output up to 32 MiB per cluster is accepted with save_output (10 MiB without it or jq)."`
}
