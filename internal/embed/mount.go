package embed

import (
	"context"
	"fmt"

	kmcpapi "github.com/containers/kubernetes-mcp-server/pkg/api"
	kmcpconfig "github.com/containers/kubernetes-mcp-server/pkg/config"
	kmcpkube "github.com/containers/kubernetes-mcp-server/pkg/kubernetes"
	kmcp "github.com/containers/kubernetes-mcp-server/pkg/mcp"
	"github.com/containers/kubernetes-mcp-server/pkg/toolsets"
	gosdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MountTools mounts every registered kubernetes-mcp-server tool onto
// ourServer, wrapped with our CAPI multi-cluster selection logic via
// resolver: each tool's "cluster" parameter is one of our composite target
// strings, resolved by capiProvider through resolver.
//
// Tools the upstream marks read-only are served by a second provider whose
// clients carry the minted read-only token instead of admin credentials, so
// the read-only RBAC (everything readable except the configured denied
// resources, Secrets by default) binds them like it binds
// exec_in_cluster_readonly. Every other tool keeps admin credentials.
//
// Their generic "targets_list" tool is skipped: list_clusters already
// covers the same information, with more detail (namespace, custom_fields)
// and the exact target string ready-made, so a second, less-detailed
// listing tool would only add confusion.
func MountTools(ctx context.Context, ourServer *gosdkmcp.Server, resolver TargetResolver) error {
	cfg := kmcpconfig.New()
	provider := newCapiProvider(resolver, cfg, false)
	readonlyProvider := newCapiProvider(resolver, cfg, true)

	configuration := kmcp.Configuration{Config: cfg}
	adminHandle, err := kmcp.NewServer(ctx, configuration, provider)
	if err != nil {
		return fmt.Errorf("embed: constructing kubernetes-mcp-server handle: %w", err)
	}
	readonlyHandle, err := kmcp.NewServer(ctx, configuration, readonlyProvider)
	if err != nil {
		return fmt.Errorf("embed: constructing read-only kubernetes-mcp-server handle: %w", err)
	}

	mutate := kmcp.WithTargetParameter(provider.GetDefaultTarget(), provider.GetTargetParameterName(), provider.IsMultiTarget())

	// Target-compatibility filtering stays off: we don't do live per-target
	// GVK discovery, so tools stay visible regardless of which CRDs happen
	// to be installed on any one target.
	toolsetContext := kmcpapi.ToolsetContext{
		Inspector:                         kmcpkube.NewClusterInspector(provider),
		Config:                            cfg,
		TargetCompatibilityFiltersEnabled: false,
	}

	for _, ts := range toolsets.Toolsets() {
		for _, tool := range ts.GetTools(ctx, toolsetContext) {
			if tool.IsTargetListProvider() {
				continue
			}
			mutated := mutate(tool)
			readOnly := mutated.Tool.Annotations.ReadOnlyHint != nil && *mutated.Tool.Annotations.ReadOnlyHint
			handle := adminHandle
			if readOnly {
				handle = readonlyHandle
			}
			goTool, handler, err := kmcp.ServerToolToGoSdkTool(handle, mutated)
			if err != nil {
				return fmt.Errorf("embed: converting tool %q: %w", mutated.Tool.Name, err)
			}
			if mutated.IsClusterAware() {
				goTool, handler = wrapFanOut(goTool, handler, resolver, readOnly)
			}
			ourServer.AddTool(goTool, handler)
		}
	}
	return nil
}
