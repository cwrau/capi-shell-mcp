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
	if err := MountTools(context.Background(), srv, &fakeResolver{}); err != nil {
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
	if err := MountTools(context.Background(), srv, resolver); err != nil {
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
