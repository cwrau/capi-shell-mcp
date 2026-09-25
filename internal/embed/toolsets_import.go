package embed

// Blank-importing every toolset subpackage registers all of them via each
// package's own init(), so MountTools always mounts whatever
// kubernetes-mcp-server ships — new toolsets they add later appear here
// automatically, with no code change on our side.
import (
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/core"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/helm"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/kcp"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/kiali"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/kubevirt"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/netobserv"
	_ "github.com/containers/kubernetes-mcp-server/pkg/toolsets/tekton"
)
