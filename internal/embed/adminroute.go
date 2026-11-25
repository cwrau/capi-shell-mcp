package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pathArgs are the arguments the upstream tools put into the path of the
// request they make (api/v1/nodes/<name>/proxy/..., the kubevirt
// subresources). Upstream joins and cleans them as path elements, so a value
// with a slash or ".." would aim the request somewhere else: with admin
// credentials that must not be possible, whatever the tool means to read.
var pathArgs = []string{"name", "namespace"}

// resourceName is what Kubernetes allows as the name of a node, namespace or
// VirtualMachineInstance (a DNS subdomain): lower case letters, digits, '-'
// and '.', and no other character, a slash or '..' above all.
var resourceName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// onlyPlainNames refuses a call whose path arguments are not plain resource
// names, before anything is resolved or sent. It wraps the per-cluster
// handler of a tool that runs with admin credentials.
func onlyPlainNames(next mcp.ToolHandler) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]any
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return errorResult("arguments are not a JSON object: " + err.Error()), nil
			}
		}
		for _, key := range pathArgs {
			v, present := args[key]
			if !present {
				continue
			}
			s, isString := v.(string)
			if !isString || len(s) > 253 || !resourceName.MatchString(s) {
				return errorResult(fmt.Sprintf("%s must be a plain resource name (lower case letters, digits, '-' and '.'), got %v", key, v)), nil
			}
		}
		return next(ctx, req)
	}
}
