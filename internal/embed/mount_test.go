package embed

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMountToolsRegistersManyToolsWithTheClustersParameter(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.1"}, nil)
	if _, err := MountTools(context.Background(), srv, &fakeResolver{}); err != nil {
		t.Fatalf("MountTools: %v", err)
	}

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

	var names []string
	var sawClustersParam bool
	var sawRequiredClustersTool string
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("Tools: %v", err)
		}
		names = append(names, tool.Name)
		if tool.Name == "targets_list" {
			t.Error("targets_list should have been filtered out")
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			continue
		}
		if props, ok := schema["properties"].(map[string]any); ok {
			if _, ok := props["cluster"]; ok {
				t.Errorf("tool %q still declares a bare 'cluster' parameter, want 'clusters'", tool.Name)
			}
			if _, ok := props["clusters"]; ok {
				sawClustersParam = true
			}
		}
		if required, ok := schema["required"].([]any); ok && sawRequiredClustersTool == "" {
			for _, r := range required {
				if r == "clusters" {
					sawRequiredClustersTool = tool.Name
				}
			}
		}
	}

	if len(names) == 0 {
		t.Fatal("no tools were mounted")
	}
	if !sawClustersParam {
		t.Errorf("no mounted tool declared a %q parameter; tools were: %v", "clusters", names)
	}
	if sawRequiredClustersTool == "" {
		t.Error("no mounted tool requires 'clusters' — expected at least one non-read-only (e.g. destructive) tool")
	}
}

func TestMountToolsCallReturnsCleanErrorForUnknownTarget(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.1"}, nil)
	resolver := &fakeResolver{err: errors.New("no such cluster")}
	if _, err := MountTools(context.Background(), srv, resolver); err != nil {
		t.Fatalf("MountTools: %v", err)
	}

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

	// Find a tool that requires 'clusters' (a non-read-only tool): those
	// pass entries straight through as exact targets, so an unresolvable
	// one exercises capiProvider.GetDerivedKubernetes's error path — a
	// read-only tool would instead just expand to zero matches silently.
	var destructiveTool string
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("Tools: %v", err)
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			continue
		}
		required, ok := schema["required"].([]any)
		if !ok {
			continue
		}
		for _, r := range required {
			if r == "clusters" {
				destructiveTool = tool.Name
			}
		}
		if destructiveTool != "" {
			break
		}
	}
	if destructiveTool == "" {
		t.Fatal("no mounted tool requires 'clusters'; cannot probe the error path")
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      destructiveTool,
		Arguments: map[string]any{"clusters": []string{"nope/ctx/ns/c1"}},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatalf("result = %+v, want isError=true for an unresolvable target", result)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "nope/ctx/ns/c1") {
		t.Errorf("error text = %q, want it to mention the target", text)
	}
}

// A tool the upstream marks read-only must be served with the minted
// read-only kubeconfig, every other tool with the admin one.
func TestMountToolsRoutesReadOnlyToolsToReadonlyCredentials(t *testing.T) {
	tests := []struct {
		tool         string
		wantAdmin    int
		wantReadonly int
	}{
		{tool: "resources_list", wantAdmin: 0, wantReadonly: 1},
		{tool: "resources_delete", wantAdmin: 1, wantReadonly: 0},
		// Reads, but through nodes/proxy and the kubevirt guest-info
		// subresources, which the read-only role withholds.
		{tool: "nodes_stats_summary", wantAdmin: 1, wantReadonly: 0},
		{tool: "nodes_log", wantAdmin: 1, wantReadonly: 0},
		{tool: "vm_guest_info", wantAdmin: 1, wantReadonly: 0},
		// pods/log is in the role.
		{tool: "pods_log", wantAdmin: 0, wantReadonly: 1},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			resolver := &fakeResolver{err: errors.New("stop here")}
			srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.1"}, nil)
			if _, err := MountTools(context.Background(), srv, resolver); err != nil {
				t.Fatalf("MountTools: %v", err)
			}
			// Mounting builds every toolset, and some of them ask the cluster
			// inspector (which serves the default target with admin rights)
			// for credentials on the way. What matters here is which
			// credentials the tool call itself uses, so count from now on.
			baseAdmin, baseReadonly := resolver.calls(), resolver.readonlyCallCount()
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

			_, err = session.CallTool(ctx, &mcp.CallToolParams{
				Name: tt.tool,
				Arguments: map[string]any{
					"apiVersion": "v1", "kind": "Secret", "name": "x", "namespace": "default",
					"clusters": []string{"prod/ctx-a/ns/c1"},
				},
			})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			gotAdmin, gotReadonly := resolver.calls()-baseAdmin, resolver.readonlyCallCount()-baseReadonly
			if gotAdmin != tt.wantAdmin || gotReadonly != tt.wantReadonly {
				t.Errorf("admin=%d readonly=%d, want admin=%d readonly=%d",
					gotAdmin, gotReadonly, tt.wantAdmin, tt.wantReadonly)
			}
		})
	}
}

// A tool that reads with admin credentials takes exact targets, so it cannot
// be broadcast, and refuses path arguments that would redirect its request.
func TestAdminRoutedReadToolsNeedExactTargetsAndPlainNames(t *testing.T) {
	session := connectedClient(t, &fakeResolver{err: errors.New("stop here")})
	ctx := context.Background()

	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("Tools: %v", err)
		}
		if tool.Name != "nodes_stats_summary" && tool.Name != "pods_log" {
			continue
		}
		schema, _ := tool.InputSchema.(map[string]any)
		required, _ := schema["required"].([]any)
		if got, want := containsAny(required, "clusters"), tool.Name == "nodes_stats_summary"; got != want {
			t.Errorf("%s: clusters required = %v, want %v", tool.Name, got, want)
		}
	}

	for _, name := range []string{"../pods/x", "a/b", "x/proxy/exec", "node?x=1", "UPPER", "", "-lead"} {
		resolver := &fakeResolver{err: errors.New("stop here")}
		s := connectedClient(t, resolver)
		base := resolver.calls()
		result, err := s.CallTool(ctx, &mcp.CallToolParams{
			Name:      "nodes_stats_summary",
			Arguments: map[string]any{"name": name, "clusters": []string{"prod/ctx-a/ns/c1"}},
		})
		if err != nil {
			t.Fatalf("name %q: CallTool: %v", name, err)
		}
		text := ""
		if len(result.Content) > 0 {
			text, _ = result.Content[0].(*mcp.TextContent).Text, true
		}
		if !result.IsError || !strings.Contains(text, "plain resource name") {
			t.Errorf("name %q: result %+v, want a refusal of the name", name, text)
		}
		if resolver.calls() != base {
			t.Errorf("name %q: the target was resolved, the request was not refused first", name)
		}
	}
}

func TestAdminRoutedReadToolsAcceptOrdinaryNodeNames(t *testing.T) {
	session := connectedClient(t, &fakeResolver{err: errors.New("stop here")})
	for _, name := range []string{"node-1", "example-s4-default-qpfcs-4cc75-qp7jt", "worker-0.example.internal"} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "nodes_stats_summary",
			Arguments: map[string]any{"name": name, "clusters": []string{"prod/ctx-a/ns/c1"}},
		})
		if err != nil {
			t.Fatalf("name %q: CallTool: %v", name, err)
		}
		if text := result.Content[0].(*mcp.TextContent).Text; strings.Contains(text, "plain resource name") {
			t.Errorf("name %q was refused: %s", name, text)
		}
	}
}
