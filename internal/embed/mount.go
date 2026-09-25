package embed

import (
	"context"
	"fmt"

	kmcpconfig "github.com/containers/kubernetes-mcp-server/pkg/config"
	kmcp "github.com/containers/kubernetes-mcp-server/pkg/mcp"
	"github.com/containers/kubernetes-mcp-server/pkg/toolsets"
	gosdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MountTools mounts every registered kubernetes-mcp-server tool onto
// ourServer, wrapped with our CAPI multi-cluster selection logic via
// resolver: each tool's "cluster" parameter is one of our composite target
// strings, resolved by capiProvider through resolver.
//
// Their generic "targets_list" tool is skipped: list_clusters already
// covers the same information, with more detail (namespace, custom_fields)
// and the exact target string ready-made, so a second, less-detailed
// listing tool would only add confusion.
func MountTools(ctx context.Context, ourServer *gosdkmcp.Server, resolver TargetResolver) error {
	provider := newCapiProvider(resolver)

	handle, err := kmcp.NewServer(ctx, kmcp.Configuration{StaticConfig: kmcpconfig.Default()}, provider)
	if err != nil {
		return fmt.Errorf("embed: constructing kubernetes-mcp-server handle: %w", err)
	}

	mutate := kmcp.WithTargetParameter(provider.GetDefaultTarget(), provider.GetTargetParameterName(), provider.IsMultiTarget())

	for _, ts := range toolsets.Toolsets() {
		for _, tool := range ts.GetTools(provider) {
			if tool.IsTargetListProvider() {
				continue
			}
			mutated := mutate(tool)
			goTool, handler, err := kmcp.ServerToolToGoSdkTool(handle, mutated)
			if err != nil {
				return fmt.Errorf("embed: converting tool %q: %w", mutated.Tool.Name, err)
			}
			if mutated.IsClusterAware() {
				readOnly := mutated.Tool.Annotations.ReadOnlyHint != nil && *mutated.Tool.Annotations.ReadOnlyHint
				goTool, handler = wrapFanOut(goTool, handler, resolver, readOnly)
			}
			ourServer.AddTool(goTool, handler)
		}
	}
	return nil
}
