package embed

import (
	"context"

	kmcpapi "github.com/containers/kubernetes-mcp-server/pkg/api"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// noInspector is the cluster inspector the toolsets get while the tools are
// mounted. The real one asks every cluster, all at once, whether it has some
// CRD, to decide which optional tools to offer (the Perses check of the
// metrics toolset, for the timeseries tool). Asking means fetching each
// cluster's kubeconfig, starting its tunnel and calling its API, so starting
// the daemon would depend on every cluster, and on every management
// cluster's credentials, being usable at that moment. Nothing here has
// Perses, and tools are offered whatever the clusters have (target
// compatibility filtering is off), so no answer is needed: this inspector
// finds nothing, without asking anything.
type noInspector struct{}

var _ kmcpapi.ClusterInspector = noInspector{}

func (noInspector) Discovery() kmcpapi.AggregateDiscovery       { return noDiscovery{} }
func (noInspector) Unstructured() kmcpapi.AggregateUnstructured { return noUnstructured{} }
func (noInspector) BearerToken(ctx context.Context) kmcpapi.Results[string] {
	return kmcpapi.NewResults(ctx, noTargets{}, func(context.Context, string) (string, error) { return "", nil })
}

// noTargets has no targets, so a Results over it runs nothing.
type noTargets struct{}

func (noTargets) IsMultiTarget() bool                          { return false }
func (noTargets) GetTargets(context.Context) ([]string, error) { return nil, nil }
func (noTargets) GetDefaultTarget() string                     { return "" }
func (noTargets) GetTargetParameterName() string               { return targetParameterName }

// What nothing was asked about is answered as a cluster without it would: not
// found. The toolsets treat that as "not there, carry on" (netobserv falls
// back to manual configuration), and not as a failure to report.
func notFound(gr schema.GroupResource, name string) error { return apierrors.NewNotFound(gr, name) }

type noDiscovery struct{}

func (noDiscovery) ServerResourcesForGroupVersion(ctx context.Context, groupVersion string) kmcpapi.Results[*metav1.APIResourceList] {
	return kmcpapi.NewResults(ctx, noTargets{}, func(context.Context, string) (*metav1.APIResourceList, error) {
		return nil, notFound(schema.GroupResource{Resource: "apiresources"}, groupVersion)
	})
}

func (noDiscovery) ServerResourcesForGroup(ctx context.Context, group string) kmcpapi.Results[[]*metav1.APIResourceList] {
	return kmcpapi.NewResults(ctx, noTargets{}, func(context.Context, string) ([]*metav1.APIResourceList, error) {
		return nil, notFound(schema.GroupResource{Resource: "apiresources"}, group)
	})
}

type noUnstructured struct{}

func (noUnstructured) Resource(gvr schema.GroupVersionResource) kmcpapi.AggregateNamespaceableResourceInterface {
	return noResource{gvr: gvr}
}

type noResource struct{ gvr schema.GroupVersionResource }

func (r noResource) Namespace(string) kmcpapi.AggregateResourceInterface { return r }

func (r noResource) Get(ctx context.Context, name string, _ metav1.GetOptions, _ ...string) kmcpapi.Results[*unstructured.Unstructured] {
	return kmcpapi.NewResults(ctx, noTargets{}, func(context.Context, string) (*unstructured.Unstructured, error) {
		return nil, notFound(r.gvr.GroupResource(), name)
	})
}

func (noResource) List(ctx context.Context, _ metav1.ListOptions) kmcpapi.Results[*unstructured.UnstructuredList] {
	return kmcpapi.NewResults(ctx, noTargets{}, func(context.Context, string) (*unstructured.UnstructuredList, error) {
		return &unstructured.UnstructuredList{}, nil
	})
}
