package capi

import (
	"context"
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func newFakeDynamicClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		clusterGVR:   "ClusterList",
		openStackGVR: "OpenStackClusterList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objects...)
}

func newCluster(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cluster.x-k8s.io/v1beta1",
		"kind":       "Cluster",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
	}}
}

func TestListClustersForContextParsesClusterList(t *testing.T) {
	dyn := newFakeDynamicClient(newCluster("ns-1", "cluster-1"), newCluster("ns-2", "cluster-2"))

	got, err := ListClustersForContext(context.Background(), dyn, "prod", "ctx-a", nil)
	if err != nil {
		t.Fatalf("ListClustersForContext: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d clusters, want 2", len(got))
	}
	for _, c := range got {
		if c.KubeconfigName != "prod" || c.Context != "ctx-a" {
			t.Errorf("cluster %+v missing kubeconfig/context", c)
		}
	}
}

func TestListClustersForContextReturnsEmptyWhenNoClusters(t *testing.T) {
	dyn := newFakeDynamicClient()

	got, err := ListClustersForContext(context.Background(), dyn, "prod", "ctx-a", nil)
	if err != nil {
		t.Fatalf("ListClustersForContext: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d clusters, want 0", len(got))
	}
}

func TestListClustersForContextPopulatesCustomFields(t *testing.T) {
	dyn := newFakeDynamicClient(newCluster("ns-1", "cluster-1"))

	got, err := ListClustersForContext(context.Background(), dyn, "prod", "ctx-a", map[string]string{
		"friendly_name": `"My Cluster"`,
		"customer_name": `"acme"`,
	})
	if err != nil {
		t.Fatalf("ListClustersForContext: %v", err)
	}
	want := map[string]string{"friendly_name": "My Cluster", "customer_name": "acme"}
	if len(got[0].CustomFields) != len(want) {
		t.Fatalf("CustomFields = %+v, want %+v", got[0].CustomFields, want)
	}
	for k, v := range want {
		if got[0].CustomFields[k] != v {
			t.Errorf("CustomFields[%q] = %q, want %q", k, got[0].CustomFields[k], v)
		}
	}
}

func TestCAPIClusterMarshalsSnakeCaseFields(t *testing.T) {
	c := CAPICluster{
		KubeconfigName: "prod",
		Context:        "ctx-a",
		Namespace:      "ns-1",
		Name:           "cluster-1",
		CustomFields:   map[string]string{"tier": "gold"},
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	for _, key := range []string{"kubeconfig", "context", "namespace", "name", "custom_fields"} {
		if _, ok := got[key]; !ok {
			t.Errorf("marshaled JSON %s missing key %q", b, key)
		}
	}
}

func TestCAPIClusterOmitsCustomFieldsWhenNil(t *testing.T) {
	c := CAPICluster{KubeconfigName: "prod", Context: "ctx-a", Namespace: "ns-1", Name: "cluster-1"}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := got["custom_fields"]; ok {
		t.Errorf("marshaled JSON %s should omit custom_fields when nil", b)
	}
}

func TestListClustersForContextSkipsNullOrEmptyCustomFieldValues(t *testing.T) {
	dyn := newFakeDynamicClient(newCluster("ns", "c"))

	got, err := ListClustersForContext(context.Background(), dyn, "prod", "ctx-a", map[string]string{
		"friendly_name": "null",
		"customer_name": `""`,
	})
	if err != nil {
		t.Fatalf("ListClustersForContext: %v", err)
	}
	if got[0].CustomFields != nil {
		t.Errorf("CustomFields = %+v, want nil", got[0].CustomFields)
	}
}
