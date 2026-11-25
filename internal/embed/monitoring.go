package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	kmcpapi "github.com/containers/kubernetes-mcp-server/pkg/api"
	kmcpconfig "github.com/containers/kubernetes-mcp-server/pkg/config"
	kmcp "github.com/containers/kubernetes-mcp-server/pkg/mcp"
	"github.com/containers/kubernetes-mcp-server/pkg/output"
	logstoolset "github.com/containers/kubernetes-mcp-server/pkg/toolsets/logs"
	metricstoolset "github.com/containers/kubernetes-mcp-server/pkg/toolsets/metrics"
	tracestoolset "github.com/containers/kubernetes-mcp-server/pkg/toolsets/traces"
	"github.com/cwrau/capi-shell-mcp/internal/monitorproxy"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// The metrics and logs tools are not per cluster upstream: they ask one
// Prometheus, Alertmanager or Loki, whose URL is a single setting. Here each
// cluster has its own, so the upstream handlers run once per cluster, with a
// configuration whose URLs lead to that cluster's services (see monitorproxy).
var monitoringToolsets = map[string]bool{
	metricstoolset.ToolsetName: true,
	logstoolset.ToolsetName:    true,
	tracestoolset.ToolsetName:  true,
}

type monitoring struct {
	resolver TargetResolver
	admin    *capiProvider
	proxy    *monitorproxy.Proxy

	mu       sync.Mutex
	configs  map[string]*kmcpconfig.Config
	upstream map[string]upstreamEntry
}

type upstreamEntry struct {
	kubeconfig string
	apiServer  *url.URL
	rt         *rest.Config
}

// newMonitoring starts the proxy, which runs until ctx ends.
func newMonitoring(ctx context.Context, resolver TargetResolver, admin *capiProvider) (*monitoring, error) {
	m := &monitoring{resolver: resolver, admin: admin, configs: map[string]*kmcpconfig.Config{}, upstream: map[string]upstreamEntry{}}
	proxy, err := monitorproxy.Start(ctx, m.reach)
	if err != nil {
		return nil, err
	}
	m.proxy = proxy
	return m, nil
}

// reach is how the proxy gets to a target's API server: with the admin
// kubeconfig of that cluster, as the service proxy needs.
func (m *monitoring) reach(ctx context.Context, target string) (*url.URL, http.RoundTripper, error) {
	kubeconfig, err := m.resolver.ResolveTarget(ctx, target)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s: %w", errUnknownTarget, target, err)
	}
	m.mu.Lock()
	entry, ok := m.upstream[target]
	m.mu.Unlock()
	if !ok || entry.kubeconfig != kubeconfig {
		restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
		if err != nil {
			return nil, nil, fmt.Errorf("embed: building rest config for target %s: %w", target, err)
		}
		apiServer, err := url.Parse(restConfig.Host)
		if err != nil {
			return nil, nil, fmt.Errorf("embed: API server address of target %s: %w", target, err)
		}
		entry = upstreamEntry{kubeconfig: kubeconfig, apiServer: apiServer, rt: restConfig}
		m.mu.Lock()
		m.upstream[target] = entry
		m.mu.Unlock()
	}
	rt, err := rest.TransportFor(entry.rt)
	if err != nil {
		return nil, nil, fmt.Errorf("embed: transport for target %s: %w", target, err)
	}
	return entry.apiServer, rt, nil
}

// config is the server configuration the upstream handlers see for one
// target: the defaults plus the URLs of that target's monitoring services.
func (m *monitoring) config(ctx context.Context, target string) (*kmcpconfig.Config, error) {
	m.mu.Lock()
	cfg, ok := m.configs[target]
	m.mu.Unlock()
	if ok {
		return cfg, nil
	}
	// kubeconfig mode: with no bearer token in the admin kubeconfig nothing
	// is sent (the proxy ignores the header anyway). The default mode warns on
	// every call that the request carries none.
	toml := fmt.Sprintf(`
[toolset_configs."%[1]s"]
auth_mode = "kubeconfig"
prometheus_url = %[2]q
alertmanager_url = %[3]q

[toolset_configs."%[4]s"]
auth_mode = "kubeconfig"
loki_url = %[5]q

[toolset_configs."%[6]s"]
auth_mode = "kubeconfig"
tempo_url = %[7]q
`, metricstoolset.ToolsetName, m.proxy.URL(target, monitorproxy.Prometheus), m.proxy.URL(target, monitorproxy.Alertmanager),
		logstoolset.ToolsetName, m.proxy.URL(target, monitorproxy.Loki),
		tracestoolset.ToolsetName, m.proxy.URL(target, monitorproxy.Tempo))
	cfg, err := kmcpconfig.ReadToml(ctx, []byte(toml), kmcpconfig.WithBaseDefault())
	if err != nil {
		return nil, fmt.Errorf("embed: building monitoring config for target %s: %w", target, err)
	}
	m.mu.Lock()
	m.configs[target] = cfg
	m.mu.Unlock()
	return cfg, nil
}

// mount turns an upstream monitoring tool into a tool that takes "clusters"
// like the others. The returned handler is the per-cluster one that
// wrapFanOut expects: it reads the target from its "cluster" argument.
func (m *monitoring) mount(tool kmcpapi.ServerTool) (*mcp.Tool, mcp.ToolHandler) {
	hideDiscoveryParams(tool.Tool.InputSchema)
	goTool := &mcp.Tool{
		Name:        tool.Tool.Name,
		Description: tool.Tool.Description,
		Title:       tool.Tool.Annotations.Title,
		Annotations: &mcp.ToolAnnotations{
			Title:           tool.Tool.Annotations.Title,
			ReadOnlyHint:    true,
			DestructiveHint: tool.Tool.Annotations.DestructiveHint,
			IdempotentHint:  tool.Tool.Annotations.IdempotentHint != nil && *tool.Tool.Annotations.IdempotentHint,
			OpenWorldHint:   tool.Tool.Annotations.OpenWorldHint,
		},
		InputSchema: tool.Tool.InputSchema,
	}
	if tool.Tool.OutputSchema != nil {
		goTool.OutputSchema = tool.Tool.OutputSchema
	}

	handler := func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]any
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return nil, fmt.Errorf("embed: unmarshaling arguments: %w", err)
			}
		}
		target, _ := args[targetParameterName].(string)
		delete(args, targetParameterName)

		cfg, err := m.config(ctx, target)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		k8s, err := m.admin.GetDerivedKubernetes(ctx, target)
		if err != nil {
			return errorResult(fmt.Sprintf("target %q: %v", target, err)), nil
		}
		params := kmcpapi.ToolHandlerParams{
			Context:          ctx,
			Config:           cfg,
			KubernetesClient: k8s,
			Request:          toolRequest(args),
			ListOutput:       output.Table,
		}
		result, err := tool.Handler(params)
		if err != nil {
			return nil, err
		}
		if len(result.ContentBlocks) > 0 {
			return kmcp.NewMultiContentResult(result.ContentBlocks, result.StructuredContent, result.Error), nil
		}
		return kmcp.NewStructuredResult(result.Content, result.StructuredContent, result.Error), nil
	}
	return goTool, handler
}

type toolRequest map[string]any

func (r toolRequest) GetArguments() map[string]any { return r }

// discoveryParams are the arguments with which upstream finds a Loki or
// Tempo instance through its LokiStack/TempoStack resource. The URLs are set
// here, so they would only ask the caller for values nothing reads.
var discoveryParams = []string{"lokiNamespace", "lokiName", "tempoNamespace", "tempoName"}

func hideDiscoveryParams(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}
	for _, p := range discoveryParams {
		delete(schema.Properties, p)
	}
	schema.Required = slices.DeleteFunc(slices.Clone(schema.Required), func(r string) bool {
		return slices.Contains(discoveryParams, r)
	})
}

// isInstanceListing is a tool that lists the LokiStack or TempoStack
// resources a cluster may have; the clusters here run plain Loki and Tempo.
func isInstanceListing(tool kmcpapi.ServerTool) bool {
	return strings.HasSuffix(tool.Tool.Name, "_list_instances")
}
