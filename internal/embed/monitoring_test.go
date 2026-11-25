package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	kmcpapi "github.com/containers/kubernetes-mcp-server/pkg/api"
	kmcpconfig "github.com/containers/kubernetes-mcp-server/pkg/config"
	metricstoolset "github.com/containers/kubernetes-mcp-server/pkg/toolsets/metrics"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const monitoringTarget = "prod/ctx-a/ns/c1"

// recordingAPIServer plays a workload cluster's API server: it answers the
// service proxy paths of Prometheus and records every request path.
type recordingAPIServer struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func newRecordingAPIServer(t *testing.T) *recordingAPIServer {
	t.Helper()
	s := &recordingAPIServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.Method+" "+r.URL.RequestURI())
		s.mu.Unlock()
		const prom = "/api/v1/namespaces/monitoring/services/kube-prometheus-stack-prometheus:http-web/proxy"
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, prom+"/api/v1/label/__name__/values"):
			_, _ = w.Write([]byte(`{"status":"success","data":["node_disk_io_time_seconds_total","node_cpu_seconds_total"]}`))
		case strings.HasPrefix(r.URL.Path, prom+"/api/v1/metadata"):
			_, _ = w.Write([]byte(`{"status":"success","data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *recordingAPIServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

func monitoringKubeconfig(server string) string {
	return fmt.Sprintf(`
apiVersion: v1
kind: Config
clusters:
- name: workload
  cluster:
    server: %s
contexts:
- name: workload
  context:
    cluster: workload
    user: admin
current-context: workload
users:
- name: admin
  user:
    token: sometoken
`, server)
}

func connectedClient(t *testing.T, resolver TargetResolver) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.1"}, nil)
	if _, err := MountTools(ctx, srv, resolver); err != nil {
		t.Fatalf("MountTools: %v", err)
	}
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("Connect server: %v", err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("Connect client: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func TestMonitoringToolsTakeClustersAndQueryThatClustersPrometheus(t *testing.T) {
	api := newRecordingAPIServer(t)
	resolver := &fakeResolver{
		targets:        []string{monitoringTarget},
		kubeconfigByID: map[string]string{monitoringTarget: monitoringKubeconfig(api.URL)},
		readonlyByID:   map[string]string{monitoringTarget: monitoringKubeconfig(api.URL)},
	}
	session := connectedClient(t, resolver)
	ctx := context.Background()

	// The tool is per cluster now: "clusters" is offered, like on the other
	// read tools.
	var schema map[string]any
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("Tools: %v", err)
		}
		if tool.Name == "list_metrics" {
			schema, _ = tool.InputSchema.(map[string]any)
		}
	}
	if schema == nil {
		t.Fatal("list_metrics is not mounted")
	}
	if props, _ := schema["properties"].(map[string]any); props["clusters"] == nil {
		t.Errorf("list_metrics has no clusters parameter: %v", schema)
	}
	if required, _ := schema["required"].([]any); containsAny(required, "clusters") {
		t.Errorf("clusters is required for list_metrics, but it is a read tool: omitting it means every cluster (%v)", required)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_metrics",
		Arguments: map[string]any{"name_regex": "node_disk_.*", "clusters": []string{monitoringTarget}},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if result.IsError {
		t.Fatalf("list_metrics failed: %s (API server saw %v)", text, api.seen())
	}
	var perCluster []struct {
		Cluster string `json:"cluster"`
		Text    string `json:"text"`
		IsError bool   `json:"is_error"`
	}
	if err := json.Unmarshal([]byte(text), &perCluster); err != nil {
		t.Fatalf("result is not the per-cluster list: %v\n%s", err, text)
	}
	if len(perCluster) != 1 || perCluster[0].Cluster != monitoringTarget || perCluster[0].IsError {
		t.Fatalf("per-cluster results = %+v", perCluster)
	}
	if !strings.Contains(perCluster[0].Text, "node_disk_io_time_seconds_total") {
		t.Errorf("answer %q lacks the metric the cluster's Prometheus returned", perCluster[0].Text)
	}

	reachedProxyPath, sentRegex := false, false
	for _, p := range api.seen() {
		if strings.Contains(p, "/services/kube-prometheus-stack-prometheus:http-web/proxy/") {
			reachedProxyPath = true
		}
		// Prometheus applies the name_regex itself, as a match[] selector.
		if strings.Contains(p, "node_disk_") {
			sentRegex = true
		}
	}
	if !reachedProxyPath {
		t.Errorf("the API server never saw the Prometheus service proxy path: %v", api.seen())
	}
	if !sentRegex {
		t.Errorf("the name_regex never reached Prometheus: %v", api.seen())
	}
}

func TestMonitoringToolsRefuseAnUnknownCluster(t *testing.T) {
	session := connectedClient(t, &fakeResolver{targets: []string{monitoringTarget}, err: fmt.Errorf("no such cluster")})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_metrics",
		Arguments: map[string]any{"name_regex": "up.*", "clusters": []string{"nope/ctx/ns/c1"}},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Errorf("result = %+v, want an error for an unresolvable cluster", result)
	}
}

func containsAny(list []any, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// With the URLs fixed, the arguments that name a Loki or Tempo instance would
// be asked of the caller for nothing: they are not offered, and the tools that
// only list such instances are not mounted.
func TestMonitoringToolsDoNotAskForInstanceNames(t *testing.T) {
	session := connectedClient(t, &fakeResolver{targets: []string{monitoringTarget}})

	seen := map[string]bool{}
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("Tools: %v", err)
		}
		seen[tool.Name] = true
		schema, _ := tool.InputSchema.(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		for _, p := range discoveryParams {
			if props[p] != nil || containsAny(required, p) {
				t.Errorf("%s still has the %s argument", tool.Name, p)
			}
		}
	}
	for _, name := range []string{"tempo_search_traces", "tempo_get_trace_by_id", "loki_query_range", "list_metrics"} {
		if !seen[name] {
			t.Errorf("%s is not mounted", name)
		}
	}
	for _, name := range []string{"tempo_list_instances", "loki_list_instances"} {
		if seen[name] {
			t.Errorf("%s is mounted, but nothing here has LokiStack/TempoStack instances to list", name)
		}
	}
}

// A target that does not exist gets no configuration and no listener, however
// many of them are asked for.
func TestMonitoringBuildsNothingForAnUnknownTarget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resolver := &fakeResolver{err: fmt.Errorf("no such cluster")}
	m, err := newMonitoring(ctx, resolver, newCapiProvider(resolver, kmcpconfig.New(), true))
	if err != nil {
		t.Fatalf("newMonitoring: %v", err)
	}
	_, handler := m.mount(kmcpapi.ServerTool{
		Tool: kmcpapi.Tool{Name: "get_alerts", InputSchema: &jsonschema.Schema{Type: "object"}},
		Handler: func(kmcpapi.ToolHandlerParams) (*kmcpapi.ToolCallResult, error) {
			t.Error("the upstream handler ran for an unknown target")
			return nil, nil
		},
	})

	for i := range 20 {
		args, _ := json.Marshal(map[string]any{"cluster": fmt.Sprintf("nope/ctx/ns/c%d", i)})
		result, err := handler(ctx, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "get_alerts", Arguments: args}})
		if err != nil || !result.IsError {
			t.Fatalf("call %d: %+v, %v; want an error result", i, result, err)
		}
	}
	m.mu.Lock()
	configs := len(m.configs)
	m.mu.Unlock()
	if configs != 0 || m.proxy.Listeners() != 0 {
		t.Errorf("%d configs and %d listeners built for targets that do not exist, want none", configs, m.proxy.Listeners())
	}
}

// A reload that lands while a config is being built must not leave a config
// cached whose Alertmanager address was closed by that reload.
func TestMonitoringConfigIsNeverCachedWithAClosedAddress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resolver := &fakeResolver{}
	m, err := newMonitoring(ctx, resolver, newCapiProvider(resolver, kmcpconfig.New(), true))
	if err != nil {
		t.Fatalf("newMonitoring: %v", err)
	}

	const target = "prod/ctx-a/ns/c1"
	for round := range 500 {
		var wg sync.WaitGroup
		wg.Add(16)
		for range 8 {
			go func() { defer wg.Done(); _, _ = m.config(ctx, target) }()
		}
		for range 8 {
			go func() { defer wg.Done(); m.reset() }()
		}
		wg.Wait()

		m.mu.Lock()
		cfg, cached := m.configs[target]
		m.mu.Unlock()
		if !cached {
			continue
		}
		ext, ok := cfg.GetToolsetConfig(metricstoolset.ToolsetName)
		if !ok {
			t.Fatal("cached config has no metrics settings")
		}
		u, err := url.Parse(ext.(*metricstoolset.Config).AlertmanagerURL)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.DialTimeout("tcp", u.Host, time.Second)
		if err != nil {
			t.Fatalf("round %d: the cached config points at %s, which is closed: %v", round, u.Host, err)
		}
		conn.Close()
	}
}

// A read tool without "clusters" runs for every known cluster, and each of
// them still goes through the proxy to its own API server.
func TestMonitoringToolsWithoutClustersQueryEveryClusterThroughTheProxy(t *testing.T) {
	apiOne, apiTwo := newRecordingAPIServer(t), newRecordingAPIServer(t)
	resolver := &fakeResolver{
		targets: []string{"prod/ctx-a/ns/c1", "prod/ctx-a/ns/c2"},
		kubeconfigByID: map[string]string{
			"prod/ctx-a/ns/c1": monitoringKubeconfig(apiOne.URL),
			"prod/ctx-a/ns/c2": monitoringKubeconfig(apiTwo.URL),
		},
		readonlyByID: map[string]string{
			"prod/ctx-a/ns/c1": monitoringKubeconfig(apiOne.URL),
			"prod/ctx-a/ns/c2": monitoringKubeconfig(apiTwo.URL),
		},
	}
	session := connectedClient(t, resolver)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_metrics",
		Arguments: map[string]any{"name_regex": "node_.*"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if result.IsError {
		t.Fatalf("list_metrics failed: %s", text)
	}
	var perCluster []struct{ Cluster string }
	if err := json.Unmarshal([]byte(text), &perCluster); err != nil || len(perCluster) != 2 {
		t.Fatalf("results = %s (%v), want one per known cluster", text, err)
	}
	for name, api := range map[string]*recordingAPIServer{"c1": apiOne, "c2": apiTwo} {
		proxied := false
		for _, p := range api.seen() {
			if strings.Contains(p, "/services/kube-prometheus-stack-prometheus:http-web/proxy/api/v1/") {
				proxied = true
			}
		}
		if !proxied {
			t.Errorf("%s: its API server never saw the Prometheus service proxy request: %v", name, api.seen())
		}
	}
}
