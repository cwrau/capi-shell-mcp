package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const clustersParameterName = "clusters"

// wrapFanOut replaces goTool's single-target "cluster" parameter (added by
// kubernetes-mcp-server's own WithTargetParameter mutator) with a
// "clusters" array parameter, and wraps handler so one call can fan out
// across every cluster matching one or more composite target patterns —
// mirroring our own exec_in_clusters, but for an arbitrary embedded tool.
//
// For read-only tools (readOnly=true): clusters may be omitted (meaning
// "every known cluster"), and each entry may be a partial pattern (1 to 4
// of kubeconfig/context/namespace/name) expanded against every currently
// known cluster. For every other tool: clusters is required, and every
// entry must be a full, exact kubeconfig/context/namespace/name path —
// no implicit "all", no wildcard expansion, since a silently-broad target
// list on a destructive tool is exactly the footgun this guards against.
//
// An explicitly empty array always means "run against nothing" (a valid,
// trivial no-op) regardless of readOnly. An empty-string entry anywhere in
// the array is always a hard error.
func wrapFanOut(goTool *mcp.Tool, handler mcp.ToolHandler, resolver TargetResolver, readOnly bool) (*mcp.Tool, mcp.ToolHandler) {
	schema, _ := goTool.InputSchema.(*jsonschema.Schema)
	if schema != nil {
		if schema.Properties != nil {
			delete(schema.Properties, targetParameterName)
		} else {
			schema.Properties = make(map[string]*jsonschema.Schema)
		}
		schema.Properties[clustersParameterName] = &jsonschema.Schema{
			Type:        "array",
			Items:       &jsonschema.Schema{Type: "string"},
			Description: clustersParamDescription(readOnly),
		}
		if !readOnly {
			schema.Required = append(schema.Required, clustersParameterName)
		}
	}

	wrapped := func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]json.RawMessage
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return nil, fmt.Errorf("embed: unmarshaling arguments: %w", err)
			}
		}

		rawClusters, present := args[clustersParameterName]
		if !present {
			if !readOnly {
				return errorResult("clusters is required"), nil
			}
			all, err := resolver.ListTargets(ctx)
			if err != nil {
				return nil, err
			}
			return runFanOut(ctx, handler, req, all), nil
		}

		var patterns []string
		if err := json.Unmarshal(rawClusters, &patterns); err != nil {
			return errorResult("clusters must be an array of strings"), nil
		}
		if len(patterns) == 0 {
			return runFanOut(ctx, handler, req, nil), nil
		}
		if slices.Contains(patterns, "") {
			return errorResult("clusters entries must be non-empty strings"), nil
		}

		targets, err := capi.ResolveClusterPatterns(patterns, func() ([]capi.CAPICluster, error) {
			return listKnownClusters(ctx, resolver)
		}, !readOnly)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return runFanOut(ctx, handler, req, targets), nil
	}

	return goTool, wrapped
}

func clustersParamDescription(readOnly bool) string {
	if readOnly {
		return "One or more cluster targets, each \"kubeconfig[/context[/namespace[/name]]]\" (from list_clusters' target field, or a shorter prefix to select every matching cluster). Omit entirely to run against every known cluster; an empty array runs against none."
	}
	return "One or more cluster targets, each the full \"kubeconfig/context/namespace/name\" path (from list_clusters' target field) — no partial/prefix matching for this tool. Required; an empty array runs against none."
}

// listKnownClusters adapts resolver's plain composite-target strings back
// into structured CAPICluster values, for capi.ResolveClusterPatterns'
// prefix matching. Targets that don't parse as an exact 4-segment path are
// skipped rather than erroring the whole list — this only ever consumes
// resolver's own already-formatted output, so a bad entry here means a
// resolver bug, not a caller mistake worth failing the request over.
func listKnownClusters(ctx context.Context, resolver TargetResolver) ([]capi.CAPICluster, error) {
	knownTargets, err := resolver.ListTargets(ctx)
	if err != nil {
		return nil, err
	}
	known := make([]capi.CAPICluster, 0, len(knownTargets))
	for _, t := range knownTargets {
		kubeconfigName, context, namespace, name, err := capi.ParseTarget(t)
		if err != nil {
			continue
		}
		known = append(known, capi.CAPICluster{KubeconfigName: kubeconfigName, Context: context, Namespace: namespace, Name: name})
	}
	return known, nil
}

type perClusterToolResult struct {
	Cluster string `json:"cluster"`
	Text    string `json:"text,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	Error   string `json:"error,omitempty"`
}

// runFanOut calls handler once per target, in parallel, isolating each
// target's error into its own result entry rather than failing the whole
// call — matching our own exec_in_clusters.
func runFanOut(ctx context.Context, handler mcp.ToolHandler, req *mcp.CallToolRequest, targets []string) *mcp.CallToolResult {
	var origArgs map[string]json.RawMessage
	if len(req.Params.Arguments) > 0 {
		_ = json.Unmarshal(req.Params.Arguments, &origArgs)
	}

	results := make([]perClusterToolResult, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()
			results[i] = callOneTarget(ctx, handler, req, origArgs, target)
		}(i, target)
	}
	wg.Wait()

	b, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return errorResult(err.Error())
	}
	isError := false
	for _, r := range results {
		if r.Error != "" || r.IsError {
			isError = true
			break
		}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}, IsError: isError}
}

func callOneTarget(ctx context.Context, handler mcp.ToolHandler, req *mcp.CallToolRequest, origArgs map[string]json.RawMessage, target string) perClusterToolResult {
	synthArgs := make(map[string]json.RawMessage, len(origArgs)+1)
	maps.Copy(synthArgs, origArgs)
	delete(synthArgs, clustersParameterName)
	targetJSON, _ := json.Marshal(target)
	synthArgs[targetParameterName] = targetJSON

	rawJSON, err := json.Marshal(synthArgs)
	if err != nil {
		return perClusterToolResult{Cluster: target, Error: err.Error()}
	}

	synthReq := &mcp.CallToolRequest{
		Session: req.Session,
		Extra:   req.Extra,
		Params: &mcp.CallToolParamsRaw{
			Meta:      req.Params.Meta,
			Name:      req.Params.Name,
			Arguments: rawJSON,
		},
	}

	result, err := handler(ctx, synthReq)
	if err != nil {
		return perClusterToolResult{Cluster: target, Error: err.Error()}
	}
	return perClusterToolResult{Cluster: target, Text: firstText(result), IsError: result.IsError}
}

func firstText(result *mcp.CallToolResult) string {
	if len(result.Content) == 0 {
		return ""
	}
	if tc, ok := result.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: msg}}, IsError: true}
}
