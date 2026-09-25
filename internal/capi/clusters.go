package capi

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/itchyny/gojq"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var clusterGVR = schema.GroupVersionResource{
	Group:    "cluster.x-k8s.io",
	Version:  "v1beta1",
	Resource: "clusters",
}

var openStackGVR = schema.GroupVersionResource{
	Group:    "infrastructure.cluster.x-k8s.io",
	Version:  "v1beta1",
	Resource: "openstackclusters",
}

// CAPICluster is one CAPI workload cluster discovered via one kubeconfig
// entry's context.
type CAPICluster struct {
	// KubeconfigName is the name of the configured kubeconfig entry this
	// cluster was discovered through (config.KubeconfigSource.Name).
	KubeconfigName string `json:"kubeconfig"`
	Context        string `json:"context"`
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	// CustomFields holds the configured custom_fields jq expressions
	// evaluated against this cluster; nil if none produced a non-empty value.
	CustomFields map[string]string `json:"custom_fields,omitempty"`
}

// ListClustersForContext lists every CAPI Cluster custom resource visible
// through dyn (already scoped to one kubeconfig entry's context) and
// evaluates customFields (if any) against them in a single batched jq call.
func ListClustersForContext(ctx context.Context, dyn dynamic.Interface, kubeconfigName, context string, customFields map[string]string) ([]CAPICluster, error) {
	list, err := dyn.Resource(clusterGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("capi: listing clusters: %w", err)
	}

	clusters := make([]CAPICluster, len(list.Items))
	items := make([]any, len(list.Items))
	for i, item := range list.Items {
		clusters[i] = CAPICluster{
			KubeconfigName: kubeconfigName,
			Context:        context,
			Namespace:      item.GetNamespace(),
			Name:           item.GetName(),
		}
		items[i] = item.Object
	}

	if len(clusters) == 0 || len(customFields) == 0 {
		return clusters, nil
	}

	fieldValues, err := evalCustomFields(items, customFields)
	if err != nil {
		return nil, fmt.Errorf("capi: evaluating custom_fields: %w", err)
	}
	for i := range clusters {
		if i < len(fieldValues) && len(fieldValues[i]) > 0 {
			clusters[i].CustomFields = fieldValues[i]
		}
	}
	return clusters, nil
}

// evalCustomFields runs one jq query, built from customFields, over
// {"items": items} and returns one map of non-empty string values per item
// — batched into a single jq evaluation regardless of item count.
func evalCustomFields(items []any, customFields map[string]string) ([]map[string]string, error) {
	keys := make([]string, 0, len(customFields))
	for k := range customFields {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%q: (%s)", k, customFields[k])
	}
	queryStr := fmt.Sprintf("[.items[] | {%s}]", strings.Join(parts, ", "))

	query, err := gojq.Parse(queryStr)
	if err != nil {
		return nil, err
	}
	code, err := gojq.Compile(query)
	if err != nil {
		return nil, err
	}

	iter := code.Run(map[string]any{"items": items})
	v, ok := iter.Next()
	if !ok {
		return nil, fmt.Errorf("jq: produced no output")
	}
	if errVal, ok := v.(error); ok {
		return nil, errVal
	}

	rawResults, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("jq: expected an array result, got %T", v)
	}

	results := make([]map[string]string, len(rawResults))
	for i, raw := range rawResults {
		obj, _ := raw.(map[string]any)
		field := make(map[string]string)
		for k, fv := range obj {
			if s, ok := fv.(string); ok && s != "" {
				field[k] = s
			}
		}
		results[i] = field
	}
	return results, nil
}
