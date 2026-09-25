package capi

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
)

func newOpenStackCluster(namespace, clusterName string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta1",
		"kind":       "OpenStackCluster",
		"metadata": map[string]any{
			"name":      clusterName,
			"namespace": namespace,
			"labels":    map[string]any{"cluster.x-k8s.io/cluster-name": clusterName},
		},
		"spec": spec,
	}}
}

const cloudsYAMLFixture = `
clouds:
  openstack:
    auth:
      auth_url: https://keystone.example.com/v3
      application_credential_id: cred-id
      application_credential_secret: cred-secret
    region_name: RegionOne
`

func TestFetchOpenStackEnvUsesLabelSelectorAndExtractsEnvVars(t *testing.T) {
	dyn := newFakeDynamicClient(newOpenStackCluster("ns-1", "cluster-1", map[string]any{
		"identityRef": map[string]any{"name": "os-creds"},
		"cloudName":   "openstack",
	}))
	core := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "os-creds", Namespace: "ns-1"},
		Data:       map[string][]byte{"clouds.yaml": []byte(cloudsYAMLFixture)},
	}).CoreV1()

	env, err := FetchOpenStackEnv(context.Background(), dyn, core, "ns-1", "cluster-1")
	if err != nil {
		t.Fatalf("FetchOpenStackEnv: %v", err)
	}
	if env["OS_AUTH_URL"] != "https://keystone.example.com/v3" {
		t.Errorf("OS_AUTH_URL = %q", env["OS_AUTH_URL"])
	}
	if env["OS_APPLICATION_CREDENTIAL_ID"] != "cred-id" {
		t.Errorf("OS_APPLICATION_CREDENTIAL_ID = %q", env["OS_APPLICATION_CREDENTIAL_ID"])
	}
	if env["OS_APPLICATION_CREDENTIAL_SECRET"] != "cred-secret" {
		t.Errorf("OS_APPLICATION_CREDENTIAL_SECRET = %q", env["OS_APPLICATION_CREDENTIAL_SECRET"])
	}
	if env["OS_REGION_NAME"] != "RegionOne" {
		t.Errorf("OS_REGION_NAME = %q", env["OS_REGION_NAME"])
	}
}

func TestFetchOpenStackEnvErrorsWhenNoOpenStackClusterFound(t *testing.T) {
	dyn := newFakeDynamicClient()
	core := fake.NewClientset().CoreV1()

	_, err := FetchOpenStackEnv(context.Background(), dyn, core, "ns-1", "cluster-1")
	if err == nil || !strings.Contains(err.Error(), "No OpenStackCluster found") {
		t.Fatalf("err = %v, want 'No OpenStackCluster found'", err)
	}
}

func TestFetchAPIServerInfoReturnsFalseWhenNoItems(t *testing.T) {
	dyn := newFakeDynamicClient()
	_, _, ok, err := FetchAPIServerInfo(context.Background(), dyn, "ns-1", "cluster-1")
	if err != nil {
		t.Fatalf("FetchAPIServerInfo: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false when no OpenStackCluster items")
	}
}

func TestFetchAPIServerInfoReturnsFalseWhenNoAllowedCIDRs(t *testing.T) {
	dyn := newFakeDynamicClient(newOpenStackCluster("ns-1", "cluster-1", map[string]any{
		"controlPlaneEndpoint": map[string]any{"host": "10.0.0.1"},
	}))
	_, _, ok, err := FetchAPIServerInfo(context.Background(), dyn, "ns-1", "cluster-1")
	if err != nil {
		t.Fatalf("FetchAPIServerInfo: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false when allowedCIDRs absent")
	}
}

func TestFetchAPIServerInfoReturnsHostPortWhenAllowedCIDRsPresent(t *testing.T) {
	dyn := newFakeDynamicClient(newOpenStackCluster("ns-1", "cluster-1", map[string]any{
		"apiServerLoadBalancer": map[string]any{"allowedCIDRs": []any{"0.0.0.0/0"}},
		"controlPlaneEndpoint":  map[string]any{"host": "10.0.0.1", "port": "6443"},
	}))
	host, port, ok, err := FetchAPIServerInfo(context.Background(), dyn, "ns-1", "cluster-1")
	if err != nil {
		t.Fatalf("FetchAPIServerInfo: %v", err)
	}
	if !ok || host != "10.0.0.1" || port != "6443" {
		t.Fatalf("got (%q, %q, %v), want (10.0.0.1, 6443, true)", host, port, ok)
	}
}

func TestFetchAPIServerInfoUsesLabelSelector(t *testing.T) {
	dyn := newFakeDynamicClient(newOpenStackCluster("ns-1", "other-cluster", map[string]any{
		"apiServerLoadBalancer": map[string]any{"allowedCIDRs": []any{"0.0.0.0/0"}},
		"controlPlaneEndpoint":  map[string]any{"host": "10.0.0.2", "port": "6443"},
	}))
	_, _, ok, err := FetchAPIServerInfo(context.Background(), dyn, "ns-1", "cluster-1")
	if err != nil {
		t.Fatalf("FetchAPIServerInfo: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false: the only OpenStackCluster is labeled for a different cluster name")
	}
}
