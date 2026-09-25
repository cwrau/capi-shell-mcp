package embed

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// recordingHandler fakes the go-sdk handler ServerToolToGoSdkTool would
// normally produce: it reads the "cluster" argument (the single-target
// param wrapFanOut's synthetic per-target requests must set) and records
// each call, safely across the fan-out's parallel goroutines.
type recordingHandler struct {
	mu       sync.Mutex
	clusters []string
	fail     map[string]string // cluster -> error text, for targets that should fail
}

func (h *recordingHandler) handle(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args map[string]any
	_ = json.Unmarshal(req.Params.Arguments, &args)
	cluster, _ := args["cluster"].(string)

	h.mu.Lock()
	h.clusters = append(h.clusters, cluster)
	h.mu.Unlock()

	if msg, ok := h.fail[cluster]; ok {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: msg}}, IsError: true}, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok:" + cluster}}}, nil
}

func (h *recordingHandler) calledWith() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.clusters))
	copy(out, h.clusters)
	return out
}

func newToolWithClusterParam() *mcp.Tool {
	return &mcp.Tool{
		Name: "pods_list",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"cluster":   {Type: "string"},
				"namespace": {Type: "string"},
			},
		},
	}
}

func callWithRawArgs(t *testing.T, handler mcp.ToolHandler, argsJSON string) *mcp.CallToolResult {
	t.Helper()
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "pods_list", Arguments: json.RawMessage(argsJSON)}}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return result
}

func resultText(result *mcp.CallToolResult) string {
	if len(result.Content) == 0 {
		return ""
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		return ""
	}
	return tc.Text
}

func TestWrapFanOutReplacesClusterParamWithClustersArray(t *testing.T) {
	goTool, _ := wrapFanOut(newToolWithClusterParam(), (&recordingHandler{}).handle, &fakeResolver{}, true)
	schema := goTool.InputSchema.(*jsonschema.Schema)
	if _, ok := schema.Properties["cluster"]; ok {
		t.Error("schema still has 'cluster' property, want it removed")
	}
	prop, ok := schema.Properties["clusters"]
	if !ok || prop.Type != "array" {
		t.Fatalf("schema.Properties[clusters] = %+v, want an array property", prop)
	}
}

func TestWrapFanOutMarksClustersRequiredForNonReadOnlyTools(t *testing.T) {
	goTool, _ := wrapFanOut(newToolWithClusterParam(), (&recordingHandler{}).handle, &fakeResolver{}, false)
	schema := goTool.InputSchema.(*jsonschema.Schema)
	found := false
	for _, r := range schema.Required {
		if r == "clusters" {
			found = true
		}
	}
	if !found {
		t.Errorf("Required = %v, want it to include 'clusters' for a non-read-only tool", schema.Required)
	}
}

func TestWrapFanOutOmittedClustersMeansAllForReadOnlyTools(t *testing.T) {
	resolver := &fakeResolver{targets: []string{"prod/ctx-a/ns/c1", "prod/ctx-a/ns/c2"}}
	recorder := &recordingHandler{}
	_, handler := wrapFanOut(newToolWithClusterParam(), recorder.handle, resolver, true)

	result := callWithRawArgs(t, handler, `{"namespace":"kube-system"}`)
	if result.IsError {
		t.Fatalf("result = %+v, want isError=false", result)
	}
	if len(recorder.calledWith()) != 2 {
		t.Fatalf("handler called %v, want 2 calls (one per known target)", recorder.calledWith())
	}
}

func TestWrapFanOutOmittedClustersErrorsForNonReadOnlyTools(t *testing.T) {
	recorder := &recordingHandler{}
	_, handler := wrapFanOut(newToolWithClusterParam(), recorder.handle, &fakeResolver{}, false)

	result := callWithRawArgs(t, handler, `{}`)
	if !result.IsError {
		t.Fatal("expected isError=true when clusters is omitted for a non-read-only tool")
	}
	if len(recorder.calledWith()) != 0 {
		t.Errorf("handler should not have been called, got %v", recorder.calledWith())
	}
}

func TestWrapFanOutEmptyArrayMeansNothing(t *testing.T) {
	recorder := &recordingHandler{}
	_, handler := wrapFanOut(newToolWithClusterParam(), recorder.handle, &fakeResolver{}, true)

	result := callWithRawArgs(t, handler, `{"clusters":[]}`)
	if result.IsError {
		t.Fatalf("result = %+v, want isError=false for an explicitly empty clusters array", result)
	}
	if len(recorder.calledWith()) != 0 {
		t.Errorf("handler should not have been called, got %v", recorder.calledWith())
	}
	var results []map[string]any
	if err := json.Unmarshal([]byte(resultText(result)), &results); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("results = %v, want empty", results)
	}
}

func TestWrapFanOutRejectsEmptyStringEntry(t *testing.T) {
	recorder := &recordingHandler{}
	_, handler := wrapFanOut(newToolWithClusterParam(), recorder.handle, &fakeResolver{}, true)

	result := callWithRawArgs(t, handler, `{"clusters":["prod", ""]}`)
	if !result.IsError {
		t.Fatal("expected isError=true for an empty-string entry in clusters")
	}
	if len(recorder.calledWith()) != 0 {
		t.Errorf("handler should not have been called, got %v", recorder.calledWith())
	}
}

func TestWrapFanOutExpandsPartialPatternForReadOnlyTools(t *testing.T) {
	resolver := &fakeResolver{targets: []string{"prod/ctx-a/ns-1/c1", "prod/ctx-a/ns-2/c2", "dev/ctx-a/ns-1/c1"}}
	recorder := &recordingHandler{}
	_, handler := wrapFanOut(newToolWithClusterParam(), recorder.handle, resolver, true)

	result := callWithRawArgs(t, handler, `{"clusters":["prod/ctx-a/ns-1"]}`)
	if result.IsError {
		t.Fatalf("result = %+v, want isError=false", result)
	}
	calls := recorder.calledWith()
	if len(calls) != 1 || calls[0] != "prod/ctx-a/ns-1/c1" {
		t.Errorf("handler called with %v, want exactly [prod/ctx-a/ns-1/c1]", calls)
	}
}

func TestWrapFanOutRejectsPartialPatternForNonReadOnlyTools(t *testing.T) {
	recorder := &recordingHandler{}
	_, handler := wrapFanOut(newToolWithClusterParam(), recorder.handle, &fakeResolver{}, false)

	result := callWithRawArgs(t, handler, `{"clusters":["prod/ctx-a"]}`)
	if !result.IsError {
		t.Fatal("expected isError=true: non-read-only tools require a full exact target path")
	}
	if len(recorder.calledWith()) != 0 {
		t.Errorf("handler should not have been called, got %v", recorder.calledWith())
	}
}

func TestWrapFanOutExactPatternSkipsListTargetsForReadOnlyTools(t *testing.T) {
	resolver := &fakeResolver{listTargetsErr: errors.New("ListTargets must not be called for an already-exact target")}
	recorder := &recordingHandler{}
	_, handler := wrapFanOut(newToolWithClusterParam(), recorder.handle, resolver, true)

	result := callWithRawArgs(t, handler, `{"clusters":["prod/ctx-a/ns-1/c1"]}`)
	if result.IsError {
		t.Fatalf("result = %+v, want isError=false", result)
	}
	calls := recorder.calledWith()
	if len(calls) != 1 || calls[0] != "prod/ctx-a/ns-1/c1" {
		t.Errorf("handler called with %v, want exactly [prod/ctx-a/ns-1/c1]", calls)
	}
	if resolver.listTargetsCallCount() != 0 {
		t.Errorf("ListTargets called %d times, want 0 for an already-exact pattern", resolver.listTargetsCallCount())
	}
}

func TestWrapFanOutAcceptsExactPathForNonReadOnlyTools(t *testing.T) {
	recorder := &recordingHandler{}
	_, handler := wrapFanOut(newToolWithClusterParam(), recorder.handle, &fakeResolver{}, false)

	result := callWithRawArgs(t, handler, `{"clusters":["prod/ctx-a/ns-1/c1"]}`)
	if result.IsError {
		t.Fatalf("result = %+v, want isError=false", result)
	}
	calls := recorder.calledWith()
	if len(calls) != 1 || calls[0] != "prod/ctx-a/ns-1/c1" {
		t.Errorf("handler called with %v, want exactly [prod/ctx-a/ns-1/c1]", calls)
	}
}

func TestWrapFanOutIsolatesPerTargetFailures(t *testing.T) {
	recorder := &recordingHandler{fail: map[string]string{"prod/ctx-a/ns-1/bad": "boom"}}
	_, handler := wrapFanOut(newToolWithClusterParam(), recorder.handle, &fakeResolver{}, false)

	result := callWithRawArgs(t, handler, `{"clusters":["prod/ctx-a/ns-1/good","prod/ctx-a/ns-1/bad"]}`)
	if !result.IsError {
		t.Fatal("expected overall isError=true when one target failed")
	}
	if !strings.Contains(resultText(result), "prod/ctx-a/ns-1/good") || !strings.Contains(resultText(result), "boom") {
		t.Errorf("result text = %q, want both the successful and failed target represented", resultText(result))
	}
}
