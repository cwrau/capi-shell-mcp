package embed

import (
	"context"
	"fmt"

	"github.com/cwrau/capi-shell-mcp/internal/capi"

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
// clients impersonate the read-only ServiceAccount instead of using admin rights, so
// the read-only RBAC (everything readable except the configured denied
// resources, Secrets by default) binds them like it binds
// exec_in_cluster_readonly. Every other tool keeps admin credentials.
//
// Their generic "targets_list" tool is skipped: list_clusters already
// covers the same information, with more detail (namespace, custom_fields)
// and the exact target string ready-made, so a second, less-detailed
// listing tool would only add confusion.
//
// The returned invalidate drops the clients the tools have cached per
// cluster; call it after the resolver's configuration changed.
func MountTools(ctx context.Context, ourServer *gosdkmcp.Server, resolver TargetResolver) (invalidate func(), err error) {
	cfg := kmcpconfig.New()
	provider := newCapiProvider(resolver, cfg, false)
	readonlyProvider := newCapiProvider(resolver, cfg, true)

	configuration := kmcp.Configuration{Config: cfg}
	adminHandle, err := kmcp.NewServer(ctx, configuration, provider)
	if err != nil {
		return nil, fmt.Errorf("embed: constructing kubernetes-mcp-server handle: %w", err)
	}
	readonlyHandle, err := kmcp.NewServer(ctx, configuration, readonlyProvider)
	if err != nil {
		return nil, fmt.Errorf("embed: constructing read-only kubernetes-mcp-server handle: %w", err)
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

	mon, err := newMonitoring(ctx, resolver, readonlyProvider)
	if err != nil {
		return nil, fmt.Errorf("embed: starting the monitoring proxy: %w", err)
	}

	for _, ts := range toolsets.Toolsets() {
		for _, tool := range ts.GetTools(ctx, toolsetContext) {
			if tool.IsTargetListProvider() {
				continue
			}
			if monitoringToolsets[ts.GetName()] {
				if isInstanceListing(tool) {
					continue
				}
				goTool, handler := mon.mount(tool)
				// Read-only like the other read tools: prefixes, and no
				// "clusters" for every cluster. Clusters without the stack
				// answer with an error of their own.
				goTool, handler = wrapFanOut(goTool, handler, resolver, true)
				ourServer.AddTool(goTool, handler)
				continue
			}
			mutated := mutate(tool)
			readOnly := mutated.Tool.Annotations.ReadOnlyHint != nil && *mutated.Tool.Annotations.ReadOnlyHint
			// A read-only tool whose reads need a subresource the read-only
			// role withholds (nodes/proxy, kubevirt's guest info) cannot
			// work as the read-only account. It runs with admin credentials
			// instead, which leaves the role itself tight, and is treated
			// like every tool that has them (see below).
			handle := adminHandle
			adminForRead := readOnly && needsWithheldSubresource(mutated.RBAC)
			if readOnly && !adminForRead {
				handle = readonlyHandle
			}
			goTool, handler, err := kmcp.ServerToolToGoSdkTool(handle, mutated)
			if err != nil {
				return nil, fmt.Errorf("embed: converting tool %q: %w", mutated.Tool.Name, err)
			}
			if adminForRead {
				// Admin credentials: exact targets only, like every other
				// tool that has them, and path arguments that cannot
				// redirect the request.
				handler = onlyPlainNames(handler)
			}
			if mutated.IsClusterAware() {
				goTool, handler = wrapFanOut(goTool, handler, resolver, readOnly && !adminForRead)
			}
			ourServer.AddTool(goTool, handler)
		}
	}
	return func() {
		provider.Invalidate()
		readonlyProvider.Invalidate()
		mon.reset()
	}, nil
}

// needsWithheldSubresource reports whether a tool declares, in its RBAC
// metadata, a read on a subresource the generated read-only role does not
// grant. Metadata that does not list requirements (none, unbounded) says
// nothing either way, so the tool stays on the read-only account.
func needsWithheldSubresource(rbac *kmcpapi.RBACMetadata) bool {
	if rbac == nil || rbac.Bounded == nil {
		return false
	}
	for _, req := range rbac.Bounded.Requirements {
		if res := req.Target.Resource; res != nil && res.Subresource != "" && !capi.ReadOnlyRoleCoversSubresource(res.Subresource) {
			return true
		}
	}
	return false
}
