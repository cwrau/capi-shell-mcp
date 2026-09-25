package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const instructions = `Use list_clusters to discover clusters before calling any other tool — it returns the composite "kubeconfig/context/namespace/name" target string every other tool's "clusters" parameter accepts.

Kubeconfig entries are themselves valid cluster targets. They appear as regular entries in list_clusters results and can be targeted with exec_in_cluster(_readonly) exactly like any workload cluster. Do not assume a kubeconfig entry is unavailable or unreachable as a target.

Every tool's "clusters" parameter takes one or more targets, each "kubeconfig[/context[/namespace[/name]]]". list_clusters and get_cluster_kubeconfig(_readonly) accept partial prefixes (e.g. "prod" selects every cluster under that kubeconfig) and treat an omitted "clusters" as "every known cluster" — they can't mutate workload state. exec_in_cluster(_readonly) always requires the full exact path for every entry, with no partial matching and no implicit "all", since the command being run is opaque.

Default to exec_in_cluster_readonly for read/check/status/debug tasks. Only use exec_in_cluster when a write, OpenStack credentials, or a privileged pod (e.g. kubectl debug node) is needed.`

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
		Description: "Return a read-only kubeconfig for one or more CAPI workload clusters, as a JSON array of {cluster, kubeconfig}, using the TokenRequest API. Always applies ServiceAccount capi-shell-mcp-read-only to each workload cluster. Token lifetime matches kubeconfig_ttl.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ClusterFilterInput) (*mcp.CallToolResult, any, error) {
		r, err := ts.GetClusterKubeconfigReadonly(ctx, in)
		return r, nil, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "exec_in_cluster",
		Description: "Run a command against one or more CAPI workload clusters in parallel, with each cluster's KUBECONFIG and OpenStack credentials set as env vars. Returns a JSON array of per-cluster results. Kubeconfig is cached; OS credentials are fetched fresh per call.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ExecInput) (*mcp.CallToolResult, any, error) {
		r, err := ts.ExecInCluster(ctx, in)
		return r, nil, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "exec_in_cluster_readonly",
		Description: "Run a command against one or more CAPI workload clusters in parallel, using a read-only kubeconfig for each. Returns a JSON array of per-cluster results. Always applies ServiceAccount capi-shell-mcp-read-only via TokenRequest API. No OpenStack credentials are injected.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ExecInput) (*mcp.CallToolResult, any, error) {
		r, err := ts.ExecInClusterReadonly(ctx, in)
		return r, nil, err
	})

	return srv
}
