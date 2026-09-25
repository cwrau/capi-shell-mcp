package capi

import (
	"fmt"
	"strings"
)

// FormatTarget builds the composite target identifier used to hand a CAPI
// cluster to an embedded (kubernetes-mcp-server) tool's cluster-selector
// parameter.
func FormatTarget(c CAPICluster) string {
	return strings.Join([]string{c.KubeconfigName, c.Context, c.Namespace, c.Name}, "/")
}

// ParseTarget splits a composite target identifier back into its four
// fields, erroring if it doesn't have exactly that shape.
func ParseTarget(target string) (kubeconfigName, context, namespace, name string, err error) {
	parts := strings.Split(target, "/")
	if len(parts) != 4 {
		return "", "", "", "", fmt.Errorf("capi: invalid target %q: want \"kubeconfig/context/namespace/name\"", target)
	}
	return parts[0], parts[1], parts[2], parts[3], nil
}
