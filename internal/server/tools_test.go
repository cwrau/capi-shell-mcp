package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
	"github.com/cwrau/capi-shell-mcp/internal/config"
	"github.com/cwrau/capi-shell-mcp/internal/proxy"
	"github.com/cwrau/capi-shell-mcp/internal/shell"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	rbacv1client "k8s.io/client-go/kubernetes/typed/rbac/v1"
	k8stesting "k8s.io/client-go/testing"
)

var (
	clusterGVR   = schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta1", Resource: "clusters"}
	openStackGVR = schema.GroupVersionResource{Group: "infrastructure.cluster.x-k8s.io", Version: "v1beta1", Resource: "openstackclusters"}
)

func newCluster(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cluster.x-k8s.io/v1beta1",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
	}}
}

func newDynamicClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		clusterGVR:   "ClusterList",
		openStackGVR: "OpenStackClusterList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objects...)
}

type fakeClients struct {
	dyn  *dynamicfake.FakeDynamicClient
	core *fake.Clientset
}

// newFakeToolServer builds a ToolServer wired to a single kubeconfig entry
// "prod" (context "ctx-a", namespace "ns-1") backed by fake
// dynamic/typed clients, seeded with a CAPI Cluster object and matching
// kubeconfig Secret per name in clusterNames.
func newFakeToolServer(t *testing.T, clusterNames ...string) (*ToolServer, *fakeClients) {
	t.Helper()

	objs := make([]runtime.Object, 0, len(clusterNames))
	for _, name := range clusterNames {
		objs = append(objs, newCluster("ns-1", name))
	}
	dyn := newDynamicClient(objs...)

	const adminKubeconfigFixture = `apiVersion: v1
kind: Config
clusters:
- name: workload
  cluster:
    server: https://10.0.0.1:6443
    certificate-authority-data: dGVzdC1jYQ==
contexts:
- name: admin
  context:
    cluster: workload
    user: admin
current-context: admin
users:
- name: admin
  user:
    token: old-admin-token
`
	secrets := make([]runtime.Object, 0, len(clusterNames))
	for _, name := range clusterNames {
		secrets = append(secrets, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-kubeconfig", Namespace: "ns-1"},
			Data:       map[string][]byte{"value": []byte(adminKubeconfigFixture)},
		})
	}
	core := fake.NewClientset(secrets...)
	core.PrependReactor("create", "serviceaccounts", tokenReactor("faketoken"))

	fc := &fakeClients{dyn: dyn, core: core}

	cfg := &config.AppConfig{
		Kubeconfigs: map[string]config.KubeconfigSource{
			"prod": {Name: "prod", Contexts: []string{"ctx-a"}},
		},
		Cache: config.CacheConfig{ClusterListTTLSeconds: 300, KubeconfigTTLSeconds: 3600},
	}

	clientsForContext := func(config.KubeconfigSource, string) (*Clients, error) {
		return &Clients{Dynamic: fc.dyn, Core: fc.core.CoreV1(), Rbac: fc.core.RbacV1()}, nil
	}

	proxyMgr := proxy.New(noopClock{}, shell.OSExecer{}, proxy.DefaultSpawnProcess, func() bool { return false })

	s := NewToolServer(cfg, capi.NewStore(300*time.Second, 3600*time.Second), proxyMgr, &fakeExecer{}, clientsForContext)
	// The fixture's kubeconfig Secrets hold placeholder YAML with no
	// cluster/server info, so real client construction from that YAML
	// would fail — point read-only client construction at the same fake
	// clientset used for the kubeconfig entry instead.
	s.clientsFromReadonlySource = func(string) (corev1client.CoreV1Interface, rbacv1client.RbacV1Interface, error) {
		return fc.core.CoreV1(), fc.core.RbacV1(), nil
	}
	return s, fc
}

type noopClock struct{}

func (noopClock) AfterFunc(time.Duration, func()) proxy.Timer { return noopTimer{} }

type noopTimer struct{}

func (noopTimer) Stop() bool { return true }

// fakeExecer simulates running a command: always exits 0 with a canned
// stdout, unless scriptedExitCode is set.
type fakeExecer struct {
	scriptedExitCode int
	scriptedStderr   string
}

func (e *fakeExecer) ExecFile(_ context.Context, file string, _ []string, _ shell.Options) (shell.Output, error) {
	if e.scriptedExitCode != 0 {
		return shell.Output{}, &shell.ExitError{ExitCode: e.scriptedExitCode, Stderr: e.scriptedStderr}
	}
	return shell.Output{Stdout: "ok: " + file}, nil
}

func tokenReactor(token string) k8stesting.ReactionFunc {
	return func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "token" {
			return false, nil, nil
		}
		return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{Token: token}}, nil
	}
}

func asText(result *mcp.CallToolResult) string {
	if len(result.Content) != 1 {
		return ""
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		return ""
	}
	return tc.Text
}

const clusterOneTarget = "prod/ctx-a/ns-1/cluster-1"

func TestListClustersReturnsEmptyForUnmatchedPattern(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	result, err := s.ListClusters(context.Background(), ClusterFilterInput{Clusters: []string{"nope"}})
	if err != nil {
		t.Fatalf("ListClusters: %v", err)
	}
	if result.IsError {
		t.Fatalf("result = %+v, want isError=false (an unmatched pattern is empty, not an error)", result)
	}
	var clusters []capi.CAPICluster
	if err := json.Unmarshal([]byte(asText(result)), &clusters); err != nil {
		t.Fatalf("json.Unmarshal: %v\ntext: %s", err, asText(result))
	}
	if len(clusters) != 0 {
		t.Errorf("got %d clusters, want 0", len(clusters))
	}
}

func TestListClustersReturnsClustersAsJSON(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1", "cluster-2")
	result, err := s.ListClusters(context.Background(), ClusterFilterInput{})
	if err != nil {
		t.Fatalf("ListClusters: %v", err)
	}
	var clusters []capi.CAPICluster
	if err := json.Unmarshal([]byte(asText(result)), &clusters); err != nil {
		t.Fatalf("json.Unmarshal: %v\ntext: %s", err, asText(result))
	}
	if len(clusters) != 2 {
		t.Fatalf("got %d clusters, want 2", len(clusters))
	}
}

func TestListClustersIncludesComputedTargetField(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	result, err := s.ListClusters(context.Background(), ClusterFilterInput{})
	if err != nil {
		t.Fatalf("ListClusters: %v", err)
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(asText(result)), &entries); err != nil {
		t.Fatalf("json.Unmarshal: %v\ntext: %s", err, asText(result))
	}
	if entries[0]["target"] != clusterOneTarget {
		t.Errorf("target = %v, want %q", entries[0]["target"], clusterOneTarget)
	}
}

func TestListClustersFiltersByPattern(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1", "cluster-2")
	result, err := s.ListClusters(context.Background(), ClusterFilterInput{Clusters: []string{clusterOneTarget}})
	if err != nil {
		t.Fatalf("ListClusters: %v", err)
	}
	var clusters []capi.CAPICluster
	if err := json.Unmarshal([]byte(asText(result)), &clusters); err != nil {
		t.Fatalf("json.Unmarshal: %v\ntext: %s", err, asText(result))
	}
	if len(clusters) != 1 || clusters[0].Name != "cluster-1" {
		t.Fatalf("got %+v, want exactly cluster-1", clusters)
	}
}

func TestListClustersCachesAcrossCalls(t *testing.T) {
	s, fc := newFakeToolServer(t, "cluster-1")
	ctx := context.Background()
	if _, err := s.ListClusters(ctx, ClusterFilterInput{}); err != nil {
		t.Fatalf("first ListClusters: %v", err)
	}

	// Delete the cluster from the fake API — if the second call still sees
	// it, that proves the result came from cache, not a re-list.
	if err := fc.dyn.Resource(clusterGVR).Namespace("ns-1").Delete(ctx, "cluster-1", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	result, err := s.ListClusters(ctx, ClusterFilterInput{})
	if err != nil {
		t.Fatalf("second ListClusters: %v", err)
	}
	var clusters []capi.CAPICluster
	if err := json.Unmarshal([]byte(asText(result)), &clusters); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(clusters) != 1 {
		t.Fatalf("got %d clusters after delete, want 1 (cached)", len(clusters))
	}
}

func TestGetClusterKubeconfigIsolatesUnknownKubeconfigPerTarget(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	result, err := s.GetClusterKubeconfig(context.Background(), ClusterFilterInput{Clusters: []string{"nope/ctx-a/ns-1/cluster-1"}})
	if err != nil {
		t.Fatalf("GetClusterKubeconfig: %v", err)
	}
	if !result.IsError {
		t.Fatalf("result = %+v, want isError=true", result)
	}
	var results []kubeconfigResult
	if err := json.Unmarshal([]byte(asText(result)), &results); err != nil {
		t.Fatalf("json.Unmarshal: %v\ntext: %s", err, asText(result))
	}
	if len(results) != 1 || !strings.Contains(results[0].Error, "nope") {
		t.Fatalf("results = %+v, want one entry with an error mentioning 'nope'", results)
	}
}

func TestGetClusterKubeconfigReturnsKubeconfigYAML(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	result, err := s.GetClusterKubeconfig(context.Background(), ClusterFilterInput{Clusters: []string{clusterOneTarget}})
	if err != nil {
		t.Fatalf("GetClusterKubeconfig: %v", err)
	}
	var results []kubeconfigResult
	if err := json.Unmarshal([]byte(asText(result)), &results); err != nil {
		t.Fatalf("json.Unmarshal: %v\ntext: %s", err, asText(result))
	}
	if len(results) != 1 || results[0].Cluster != clusterOneTarget || !strings.Contains(results[0].Kubeconfig, "kind: Config") {
		t.Fatalf("results = %+v, want one entry with kubeconfig YAML", results)
	}
}

func TestExecInClusterReturnsStdoutOnSuccessWhenOpenStackClusterExists(t *testing.T) {
	s, fc := newFakeToolServer(t, "cluster-1")
	seedOpenStackCluster(t, fc, "cluster-1")

	result, err := s.ExecInCluster(context.Background(), ExecInput{
		Clusters: []string{clusterOneTarget},
		Command:  []string{"kubectl", "get", "nodes"},
	})
	if err != nil {
		t.Fatalf("ExecInCluster: %v", err)
	}
	if result.IsError {
		t.Fatalf("result = %+v, want isError=false", result)
	}
	var results []clusterExecResult
	if err := json.Unmarshal([]byte(asText(result)), &results); err != nil {
		t.Fatalf("json.Unmarshal: %v\ntext: %s", err, asText(result))
	}
	if len(results) != 1 || results[0].ExitCode == nil || *results[0].ExitCode != 0 {
		t.Fatalf("results = %+v", results)
	}
}

func TestExecInClusterErrorsWithoutOpenStackCluster(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	result, err := s.ExecInCluster(context.Background(), ExecInput{
		Clusters: []string{clusterOneTarget},
		Command:  []string{"kubectl", "get", "nodes"},
	})
	if err != nil {
		t.Fatalf("ExecInCluster: %v", err)
	}
	// Write-mode exec requires an OpenStackCluster CR, and none exists in
	// this fixture — isolated as a per-cluster error, not a whole-call
	// failure.
	if !result.IsError {
		t.Fatalf("result = %+v, want isError=true", result)
	}
}

func TestExecInClusterRejectsEmptyCommand(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	_, err := s.ExecInCluster(context.Background(), ExecInput{
		Clusters: []string{clusterOneTarget},
		Command:  []string{},
	})
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestExecInClusterRejectsEmptyClusters(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	result, err := s.ExecInCluster(context.Background(), ExecInput{Command: []string{"true"}})
	if err != nil {
		t.Fatalf("ExecInCluster: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected isError=true when clusters is omitted (strict: always required)")
	}
}

func TestExecInClusterRejectsPartialPattern(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	result, err := s.ExecInCluster(context.Background(), ExecInput{Clusters: []string{"prod"}, Command: []string{"true"}})
	if err != nil {
		t.Fatalf("ExecInCluster: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected isError=true for a partial pattern: exec tools require a full exact path")
	}
}

func TestExecInClusterReadonlyRunsWithoutOpenStackEnv(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	result, err := s.ExecInClusterReadonly(context.Background(), ExecInput{
		Clusters: []string{clusterOneTarget},
		Command:  []string{"kubectl", "get", "nodes"},
	})
	if err != nil {
		t.Fatalf("ExecInClusterReadonly: %v", err)
	}
	if result.IsError {
		t.Fatalf("result = %+v, want isError=false", result)
	}
}

func TestGetClusterKubeconfigReadonlyMintsToken(t *testing.T) {
	s, fc := newFakeToolServer(t, "cluster-1")
	fc.core.PrependReactor("create", "serviceaccounts", tokenReactor("mytoken"))

	result, err := s.GetClusterKubeconfigReadonly(context.Background(), ClusterFilterInput{Clusters: []string{clusterOneTarget}})
	if err != nil {
		t.Fatalf("GetClusterKubeconfigReadonly: %v", err)
	}
	if !strings.Contains(asText(result), "mytoken") {
		t.Fatalf("result text = %q, want it to contain the minted token", asText(result))
	}
}

func TestExecInClusterFanOutIsolatesPerClusterFailures(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1", "cluster-2")

	result, err := s.ExecInCluster(context.Background(), ExecInput{
		Clusters: []string{clusterOneTarget, "prod/ctx-a/ns-1/cluster-2"},
		Command:  []string{"kubectl", "get", "nodes"},
	})
	if err != nil {
		t.Fatalf("ExecInCluster: %v", err)
	}
	// Neither cluster has an OpenStackCluster CR, so both fail per-cluster
	// (isolated, not a whole-call failure) and isError is true overall.
	if !result.IsError {
		t.Fatalf("result = %+v, want isError=true (both clusters lack OpenStackCluster)", result)
	}
	var results []map[string]any
	if err := json.Unmarshal([]byte(asText(result)), &results); err != nil {
		t.Fatalf("json.Unmarshal: %v\ntext: %s", err, asText(result))
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r["error"] == nil {
			t.Errorf("result %+v: want a non-nil error (no OpenStackCluster CR present)", r)
		}
	}
}

func TestExecInClusterReadonlySucceedsWithoutOpenStackCluster(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	result, err := s.ExecInClusterReadonly(context.Background(), ExecInput{
		Clusters: []string{clusterOneTarget},
		Command:  []string{"kubectl", "get", "nodes"},
	})
	if err != nil {
		t.Fatalf("ExecInClusterReadonly: %v", err)
	}
	if result.IsError {
		t.Fatalf("result = %+v, want isError=false (readonly path needs no OpenStackCluster)", result)
	}
}

func seedOpenStackCluster(t *testing.T, fc *fakeClients, clusterName string) {
	t.Helper()
	osc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta1",
		"kind":       "OpenStackCluster",
		"metadata": map[string]any{
			"name":      clusterName,
			"namespace": "ns-1",
			"labels":    map[string]any{"cluster.x-k8s.io/cluster-name": clusterName},
		},
		"spec": map[string]any{
			"identityRef": map[string]any{"name": clusterName + "-cloud-config"},
		},
	}}
	if err := fc.dyn.Tracker().Add(osc); err != nil {
		t.Fatalf("seeding OpenStackCluster: %v", err)
	}
	cloudsYAML := "clouds:\n  openstack:\n    auth:\n      auth_url: https://keystone.example.com/v3\n"
	if err := fc.core.Tracker().Add(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-cloud-config", Namespace: "ns-1"},
		Data:       map[string][]byte{"clouds.yaml": []byte(cloudsYAML)},
	}); err != nil {
		t.Fatalf("seeding cloud-config secret: %v", err)
	}
}
