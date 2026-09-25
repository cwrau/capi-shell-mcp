package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBuildServerRegistersAllFiveTools(t *testing.T) {
	ts, _ := newFakeToolServer(t, "cluster-1")
	srv := BuildServer(ts)

	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("Connect server: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("Connect client: %v", err)
	}
	defer session.Close()

	names := map[string]bool{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("Tools: %v", err)
		}
		names[tool.Name] = true
	}

	want := []string{
		"list_clusters",
		"get_cluster_kubeconfig",
		"get_cluster_kubeconfig_readonly",
		"exec_in_cluster",
		"exec_in_cluster_readonly",
	}
	for _, name := range want {
		if !names[name] {
			t.Errorf("missing tool %q, registered tools: %v", name, names)
		}
	}
	if len(names) != len(want) {
		t.Errorf("registered tools = %v, want exactly %v (exec_in_clusters(_readonly) should be gone, merged into the singular tools)", names, want)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_clusters", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool list_clusters: %v", err)
	}
	if result.IsError {
		t.Fatalf("list_clusters result isError=true: %+v", result)
	}
	var clusters []capi.CAPICluster
	text := result.Content[0].(*mcp.TextContent).Text
	if err := json.Unmarshal([]byte(text), &clusters); err != nil {
		t.Fatalf("json.Unmarshal: %v\ntext: %s", err, text)
	}
	if len(clusters) != 1 {
		t.Fatalf("got %d clusters, want 1", len(clusters))
	}
}
