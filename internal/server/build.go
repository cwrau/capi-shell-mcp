package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const instructions = `Use list_clusters to discover clusters before calling any other tool — it returns the composite "kubeconfig/context/namespace/name" target string every other tool's "clusters" parameter accepts.

Kubeconfig entries are themselves valid cluster targets. They appear as regular entries in list_clusters results and can be targeted with exec_in_cluster(_readonly) exactly like any workload cluster. Do not assume a kubeconfig entry is unavailable or unreachable as a target.

Every tool's "clusters" parameter takes one or more targets, each "kubeconfig[/context[/namespace[/name]]]". The tools that can't mutate workload state — list_clusters, get_cluster_kubeconfig(_readonly), exec_in_cluster_readonly and the read-only embedded tools — accept partial prefixes (e.g. "prod" selects every cluster under that kubeconfig) and treat an omitted "clusters" as "every known cluster"; an empty array selects none. exec_in_cluster runs an arbitrary command, so it always requires the full exact path for every entry, with no partial matching and no implicit "all".

Fan-out results leave out clusters that ran fine and printed nothing, so an empty array means there was nothing to report; clusters with errors or non-zero exits are always included.

Default to exec_in_cluster_readonly for read/check/status/debug tasks — it takes kubectl arguments only (not a command) and rejects anything outside a read-only allowlist. Only use exec_in_cluster when a write, OpenStack credentials, or a privileged pod (e.g. kubectl debug node) is needed.`

// BuildServer registers all 5 bespoke tools on a fresh *mcp.Server backed
// by ts. One *mcp.Server is shared across every MCP session for the
// process lifetime — tools, cfg, and caches are all process-level state,
// never rebuilt per session.
func BuildServer(ts *ToolServer) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "capi-shell-mcp", Version: "0.1.0"}, &mcp.ServerOptions{
		Instructions: instructions,
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_clusters",
		Description: "List CAPI workload clusters across configured kubeconfigs, optionally filtered by one or more cluster patterns. Results cached per kubeconfig+context.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ClusterFilterInput) (*mcp.CallToolResult, any, error) {
		r, err := ts.ListClusters(ctx, in)
		return r, nil, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_cluster_kubeconfig",
		Description: "Return the (transformed) kubeconfig YAML for one or more CAPI workload clusters, as a JSON array of {cluster, kubeconfig}. Cached per cluster. Starts sshuttle proxy if configured.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ClusterFilterInput) (*mcp.CallToolResult, any, error) {
		r, err := ts.GetClusterKubeconfig(ctx, in)
		return r, nil, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_cluster_kubeconfig_readonly",
		Description: "Return a read-only kubeconfig for one or more CAPI workload clusters, as a JSON array of {cluster, kubeconfig}, using the TokenRequest API. Always applies ServiceAccount capi-shell-mcp-read-only to each workload cluster, bound to a ClusterRole the daemon generates from the cluster's API resources: get, list and watch on everything except the resources in the daemon's readonly.denied_resources setting (default: only Secrets; an empty list denies nothing). Resources that appear later are picked up the next time a token is minted. Token lifetime matches kubeconfig_ttl.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ClusterFilterInput) (*mcp.CallToolResult, any, error) {
		r, err := ts.GetClusterKubeconfigReadonly(ctx, in)
		return r, nil, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "exec_in_cluster",
		Description: "Run an arbitrary command against one or more CAPI workload clusters in parallel, with each cluster's KUBECONFIG and OpenStack credentials set as env vars. Returns a JSON array of per-cluster results; clusters that exited 0 with no stdout are left out. Kubeconfig is cached; OS credentials are fetched fresh per call.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ExecInput) (*mcp.CallToolResult, any, error) {
		r, err := ts.ExecInCluster(ctx, in)
		return r, nil, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "exec_in_cluster_readonly",
		Description: "Run kubectl — and only kubectl, with allowlisted read-only arguments — against one or more CAPI workload clusters in parallel, using a read-only kubeconfig for each. Accepts partial cluster prefixes, or an omitted clusters for every known cluster. Returns a JSON array of per-cluster results; clusters that exited 0 with no stdout are left out, so [] means nothing to report (errors and non-zero exits are always included). Optional jq filters JSON output in-process; save_output writes large outputs to a daemon-side file. Always applies ServiceAccount capi-shell-mcp-read-only via TokenRequest API — Access is a ClusterRole the daemon generates: get, list and watch on every API resource except those in the daemon's readonly.denied_resources setting (default: only Secrets, so use exec_in_cluster to read those). No OpenStack credentials are injected.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ReadonlyExecInput) (*mcp.CallToolResult, any, error) {
		r, err := ts.ExecInClusterReadonly(ctx, in)
		return r, nil, err
	})

	return srv
}
