package server

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
	"github.com/cwrau/capi-shell-mcp/internal/config"
	"github.com/cwrau/capi-shell-mcp/internal/proxy"
	"github.com/cwrau/capi-shell-mcp/internal/shell"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	rbacv1client "k8s.io/client-go/kubernetes/typed/rbac/v1"
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

	fc := &fakeClients{dyn: dyn, core: core}

	cfg := &config.AppConfig{
		Kubeconfigs: map[string]config.KubeconfigSource{
			"prod": {Name: "prod", Contexts: []string{"ctx-a"}},
		},
		Cache:    config.CacheConfig{ClusterListTTLSeconds: 300, KubeconfigTTLSeconds: 3600},
		ReadOnly: config.ReadOnlyConfig{DeniedResources: config.DefaultDeniedResources()},
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
	s.clientsFromReadonlySource = func(string) (corev1client.CoreV1Interface, rbacv1client.RbacV1Interface, capi.APIResourceLister, error) {
		return fc.core.CoreV1(), fc.core.RbacV1(), stubDiscovery{}, nil
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
	// scriptedStdout is returned on success; empty means "ok: <file>",
	// unless silent is set (success with no stdout at all).
	scriptedStdout string
	silent         bool
	// okStderr is written to stderr on a successful exit.
	okStderr string

	mu            sync.Mutex
	calls         int
	lastFile      string
	lastArgs      []string
	lastEnv       []string
	lastMaxOutput int
}

func (e *fakeExecer) ExecFile(_ context.Context, file string, args []string, opts shell.Options) (shell.Output, error) {
	e.mu.Lock()
	e.calls++
	e.lastFile, e.lastArgs, e.lastEnv, e.lastMaxOutput = file, args, opts.Env, opts.MaxOutputBytes
	e.mu.Unlock()

	if e.scriptedExitCode != 0 {
		return shell.Output{}, &shell.ExitError{ExitCode: e.scriptedExitCode, Stderr: e.scriptedStderr}
	}
	stdout := "ok: " + file
	switch {
	case e.silent:
		stdout = ""
	case e.scriptedStdout != "":
		stdout = e.scriptedStdout
	}
	return shell.Output{Stdout: stdout, Stderr: e.okStderr}, nil
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
	if len(results) != 1 || results[0].Stdout != "ok: kubectl" || results[0].ExitCode != 0 {
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
	result, err := s.ExecInClusterReadonly(context.Background(), ReadonlyExecInput{
		Clusters: []string{clusterOneTarget},
		Args:     []string{"get", "nodes"},
	})
	if err != nil {
		t.Fatalf("ExecInClusterReadonly: %v", err)
	}
	if result.IsError {
		t.Fatalf("result = %+v, want isError=false", result)
	}
}

// readonlyResults runs ExecInClusterReadonly and decodes its per-cluster
// results.
func readonlyResults(t *testing.T, s *ToolServer, in ReadonlyExecInput) ([]clusterExecResult, *mcp.CallToolResult) {
	t.Helper()
	result, err := s.ExecInClusterReadonly(context.Background(), in)
	if err != nil {
		t.Fatalf("ExecInClusterReadonly: %v", err)
	}
	var results []clusterExecResult
	if err := json.Unmarshal([]byte(asText(result)), &results); err != nil {
		t.Fatalf("result is not a JSON array: %v\ntext: %s", err, asText(result))
	}
	return results, result
}

func TestExecInClusterReadonlyRunsOnlyKubectlWithTheGivenArgsAndAScrubbedEnv(t *testing.T) {
	t.Setenv("CAPI_TEST_PARENT_SECRET", "leak-me")
	s, _ := newFakeToolServer(t, "cluster-1")
	execer := s.execer.(*fakeExecer)

	results, _ := readonlyResults(t, s, ReadonlyExecInput{
		Clusters: []string{clusterOneTarget},
		Args:     []string{"get", "pods", "-n", "kube-system", "-o", "json"},
	})
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	if execer.lastFile != "kubectl" {
		t.Errorf("ran %q, want kubectl", execer.lastFile)
	}
	if strings.Join(execer.lastArgs, " ") != "get pods -n kube-system -o json" {
		t.Errorf("args = %v", execer.lastArgs)
	}
	for _, kv := range execer.lastEnv {
		if strings.Contains(kv, "leak-me") {
			t.Errorf("parent env leaked into kubectl: %q", kv)
		}
	}
}

func TestExecInClusterReadonlyRejectsAnythingButAllowedKubectlArgs(t *testing.T) {
	cases := map[string][]string{
		"arbitrary binary":     {"sh", "-c", "id"},
		"kubeconfig override":  {"get", "pods", "--kubeconfig=/home/u/.config/kube/example"},
		"local file via -o":    {"get", "pods", "-o", "go-template-file=/etc/passwd"},
		"plugin dispatch":      {"oidc-login", "get-token"},
		"write verb":           {"delete", "pod", "foo"},
		"empty":                {},
		"leading flag hide":    {"--token=x", "get", "pods"},
		"unlisted flag on get": {"get", "pods", "-f", "/etc/passwd"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newFakeToolServer(t, "cluster-1")
			execer := s.execer.(*fakeExecer)

			result, err := s.ExecInClusterReadonly(context.Background(), ReadonlyExecInput{
				Clusters: []string{clusterOneTarget},
				Args:     args,
			})
			if err != nil {
				t.Fatalf("ExecInClusterReadonly: %v", err)
			}
			if !result.IsError || !strings.Contains(asText(result), "args rejected") {
				t.Fatalf("result = %q (isError=%v), want an 'args rejected' error", asText(result), result.IsError)
			}
			if execer.calls != 0 {
				t.Errorf("a rejected call ran %d command(s), want none", execer.calls)
			}
		})
	}
}

func TestExecInClusterReadonlyOmittedClustersMeansEveryKnownCluster(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1", "cluster-2")
	results, _ := readonlyResults(t, s, ReadonlyExecInput{Args: []string{"get", "nodes"}})
	if len(results) != 2 {
		t.Fatalf("omitted clusters: got %d results, want 2 (every known cluster)", len(results))
	}
}

func TestExecInClusterReadonlyPrefixSelectsMatchingClusters(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1", "cluster-2")
	results, _ := readonlyResults(t, s, ReadonlyExecInput{Clusters: []string{"prod/ctx-a/ns-1"}, Args: []string{"get", "nodes"}})
	if len(results) != 2 {
		t.Fatalf("prefix: got %d results, want 2", len(results))
	}
	results, _ = readonlyResults(t, s, ReadonlyExecInput{Clusters: []string{"other"}, Args: []string{"get", "nodes"}})
	if len(results) != 0 {
		t.Fatalf("non-matching prefix: got %d results, want 0", len(results))
	}
}

func TestExecInClusterReadonlyEmptyClustersArrayMeansNone(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	execer := s.execer.(*fakeExecer)
	result, _ := readonlyResults(t, s, ReadonlyExecInput{Clusters: []string{}, Args: []string{"get", "nodes"}})
	if len(result) != 0 || execer.calls != 0 {
		t.Fatalf("results=%v calls=%d, want none", result, execer.calls)
	}
}

func TestExecResultsOmitEmptyFieldsAndExitCodeZero(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	_, result := readonlyResults(t, s, ReadonlyExecInput{Clusters: []string{clusterOneTarget}, Args: []string{"get", "nodes"}})
	var raw []map[string]any
	if err := json.Unmarshal([]byte(asText(result)), &raw); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("raw = %v", raw)
	}
	for _, absent := range []string{"stderr", "exit_code", "error", "stdout_file", "stdout_bytes"} {
		if _, present := raw[0][absent]; present {
			t.Errorf("successful result has %q, want it omitted: %v", absent, raw[0])
		}
	}
	if raw[0]["stdout"] == nil {
		t.Errorf("stdout missing: %v", raw[0])
	}
}

func TestExecClustersThatSucceededWithNoOutputAreLeftOut(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1", "cluster-2")
	s.execer.(*fakeExecer).silent = true
	results, result := readonlyResults(t, s, ReadonlyExecInput{Args: []string{"get", "pods", "-l", "none=1"}})
	if len(results) != 0 || strings.TrimSpace(asText(result)) != "[]" {
		t.Fatalf("results = %v, text = %q, want []", results, asText(result))
	}
	if result.IsError {
		t.Error("nothing to report must not be an error")
	}
}

func TestExecStderrIsIgnoredOnSuccessWithEmptyStdout(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	execer := s.execer.(*fakeExecer)
	execer.silent, execer.okStderr = true, "No resources found\n"
	results, _ := readonlyResults(t, s, ReadonlyExecInput{Clusters: []string{clusterOneTarget}, Args: []string{"get", "pods"}})
	if len(results) != 0 {
		t.Fatalf("results = %+v, want 'No resources found' noise dropped", results)
	}
}

func TestExecStderrIsKeptAlongsideRealStdout(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	execer := s.execer.(*fakeExecer)
	execer.okStderr = "Warning: deprecated\n"
	results, _ := readonlyResults(t, s, ReadonlyExecInput{Clusters: []string{clusterOneTarget}, Args: []string{"get", "pods"}})
	if len(results) != 1 || results[0].Stderr != "Warning: deprecated\n" {
		t.Fatalf("results = %+v, want the warning kept with real output", results)
	}
}

func TestExecNonZeroExitIsKeptWithItsExitCode(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	execer := s.execer.(*fakeExecer)
	execer.scriptedExitCode, execer.scriptedStderr = 1, "boom"
	results, result := readonlyResults(t, s, ReadonlyExecInput{Clusters: []string{clusterOneTarget}, Args: []string{"get", "pods"}})
	if len(results) != 1 || results[0].ExitCode != 1 || results[0].Stderr != "boom" || !result.IsError {
		t.Fatalf("results = %+v isError=%v", results, result.IsError)
	}
}

const podListJSON = `{"items":[{"metadata":{"name":"a"}},{"metadata":{"name":"b"}}]}`

func TestExecReadonlyJQFiltersJSONOutputInProcess(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	s.execer.(*fakeExecer).scriptedStdout = podListJSON
	results, _ := readonlyResults(t, s, ReadonlyExecInput{
		Clusters: []string{clusterOneTarget},
		Args:     []string{"get", "pods", "-o", "json"},
		JQ:       ".items[].metadata.name",
	})
	if len(results) != 1 || results[0].Stdout != "a\nb\n" {
		t.Fatalf("results = %+v, want raw names", results)
	}
}

func TestExecReadonlyJQRendersNonStringsAsIndentedJSON(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	s.execer.(*fakeExecer).scriptedStdout = podListJSON
	results, _ := readonlyResults(t, s, ReadonlyExecInput{
		Clusters: []string{clusterOneTarget},
		Args:     []string{"get", "pods", "-o", "json"},
		JQ:       ".items[0].metadata",
	})
	if len(results) != 1 || results[0].Stdout != "{\n  \"name\": \"a\"\n}\n" {
		t.Fatalf("results = %+v", results)
	}
}

func TestExecReadonlyJQWithNoMatchesCountsAsNothingToReport(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	s.execer.(*fakeExecer).scriptedStdout = `{"items":[]}`
	results, _ := readonlyResults(t, s, ReadonlyExecInput{
		Clusters: []string{clusterOneTarget},
		Args:     []string{"get", "pods", "-o", "json"},
		JQ:       ".items[].metadata.name",
	})
	if len(results) != 0 {
		t.Fatalf("results = %+v, want the cluster left out", results)
	}
}

func TestExecReadonlyJQOnNonJSONIsAPerClusterError(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	s.execer.(*fakeExecer).scriptedStdout = "NAME   READY\npod-a  1/1\n"
	results, result := readonlyResults(t, s, ReadonlyExecInput{
		Clusters: []string{clusterOneTarget},
		Args:     []string{"get", "pods"},
		JQ:       ".items",
	})
	if len(results) != 1 || !strings.Contains(results[0].Error, "not JSON") || !result.IsError {
		t.Fatalf("results = %+v", results)
	}
}

func TestExecReadonlyBadJQFailsTheWholeCallBeforeTouchingAnyCluster(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	execer := s.execer.(*fakeExecer)
	result, err := s.ExecInClusterReadonly(context.Background(), ReadonlyExecInput{
		Clusters: []string{clusterOneTarget},
		Args:     []string{"get", "pods", "-o", "json"},
		JQ:       ".items[",
	})
	if err != nil {
		t.Fatalf("ExecInClusterReadonly: %v", err)
	}
	if !result.IsError || !strings.Contains(asText(result), "jq: invalid expression") || execer.calls != 0 {
		t.Fatalf("text=%q isError=%v calls=%d", asText(result), result.IsError, execer.calls)
	}
}

func TestExecReadonlyJQCannotReadTheEnvironment(t *testing.T) {
	t.Setenv("CAPI_TEST_PARENT_SECRET", "leak-me")
	s, _ := newFakeToolServer(t, "cluster-1")
	s.execer.(*fakeExecer).scriptedStdout = `{}`
	results, _ := readonlyResults(t, s, ReadonlyExecInput{
		Clusters: []string{clusterOneTarget},
		Args:     []string{"get", "pods", "-o", "json"},
		JQ:       "env | tojson",
	})
	for _, r := range results {
		if strings.Contains(r.Stdout, "leak-me") {
			t.Fatalf("jq saw the daemon's environment: %q", r.Stdout)
		}
	}
}

func TestExecReadonlySaveOutputWritesAPrivateFileAndReturnsItsPath(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	s.outputDirOverride = t.TempDir()
	s.execer.(*fakeExecer).scriptedStdout = "line one\nline two\n"

	results, _ := readonlyResults(t, s, ReadonlyExecInput{
		Clusters:   []string{clusterOneTarget},
		Args:       []string{"get", "pods"},
		SaveOutput: true,
	})
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	r := results[0]
	if r.Stdout != "" || r.StdoutFile == "" || r.StdoutBytes != len("line one\nline two\n") {
		t.Fatalf("result = %+v, want stdout moved into stdout_file", r)
	}
	if !strings.HasPrefix(r.StdoutFile, s.outputDirOverride) {
		t.Errorf("stdout_file %q is outside the daemon-chosen dir %q", r.StdoutFile, s.outputDirOverride)
	}
	got, err := os.ReadFile(r.StdoutFile)
	if err != nil || string(got) != "line one\nline two\n" {
		t.Fatalf("file content = %q, err=%v", got, err)
	}
	info, _ := os.Stat(r.StdoutFile)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestExecReadonlySaveOutputAppliesAfterJQ(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	s.outputDirOverride = t.TempDir()
	s.execer.(*fakeExecer).scriptedStdout = podListJSON
	results, _ := readonlyResults(t, s, ReadonlyExecInput{
		Clusters:   []string{clusterOneTarget},
		Args:       []string{"get", "pods", "-o", "json"},
		JQ:         ".items[].metadata.name",
		SaveOutput: true,
	})
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	got, _ := os.ReadFile(results[0].StdoutFile)
	if string(got) != "a\nb\n" {
		t.Errorf("saved content = %q, want the jq-filtered output", got)
	}
}

func TestSaveOutputSweepsStaleFilesButKeepsFreshOnes(t *testing.T) {
	dir := t.TempDir()
	stale := dir + "/out-stale.txt"
	fresh := dir + "/out-fresh.txt"
	unrelated := dir + "/keep-me.txt"
	for _, p := range []string{stale, fresh, unrelated} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(unrelated, old, old); err != nil {
		t.Fatal(err)
	}

	if _, err := saveOutput(dir, "new"); err != nil {
		t.Fatalf("saveOutput: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale saved output still present (err=%v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh saved output was removed: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("a file without the saved-output prefix was removed: %v", err)
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
	result, err := s.ExecInClusterReadonly(context.Background(), ReadonlyExecInput{
		Clusters: []string{clusterOneTarget},
		Args:     []string{"get", "nodes"},
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

func TestKubeconfigToolsOmittedClustersMeansEveryKnownCluster(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1", "cluster-2")

	for name, call := range map[string]func() (*mcp.CallToolResult, error){
		"get_cluster_kubeconfig": func() (*mcp.CallToolResult, error) {
			return s.GetClusterKubeconfig(context.Background(), ClusterFilterInput{})
		},
	} {
		result, err := call()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var results []kubeconfigResult
		if err := json.Unmarshal([]byte(asText(result)), &results); err != nil {
			t.Fatalf("%s: %v\ntext: %s", name, err, asText(result))
		}
		if len(results) != 2 {
			t.Errorf("%s with clusters omitted: got %d results, want 2 (every known cluster)", name, len(results))
		}
	}
}

func TestKubeconfigToolsEmptyClustersArrayMeansNone(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	result, err := s.GetClusterKubeconfig(context.Background(), ClusterFilterInput{Clusters: []string{}})
	if err != nil {
		t.Fatalf("GetClusterKubeconfig: %v", err)
	}
	if strings.TrimSpace(asText(result)) != "[]" {
		t.Fatalf("text = %q, want []", asText(result))
	}
}

func TestSaveOutputRefusesADirectoryThatIsASymlink(t *testing.T) {
	base := t.TempDir()
	target := base + "/elsewhere"
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := base + "/capi-shell-mcp"
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}

	if _, err := saveOutput(dir, "secret"); err == nil {
		t.Fatal("saveOutput followed a symlinked output directory, want an error")
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Errorf("a file was written through the symlink: %v", entries)
	}
}

func TestSaveOutputTightensAnExistingDirectoryOthersCouldEnter(t *testing.T) {
	dir := t.TempDir() + "/capi-shell-mcp"
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}

	if _, err := saveOutput(dir, "secret"); err != nil {
		t.Fatalf("saveOutput: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("output directory mode = %o, want 700", got)
	}
}

func TestSaveOutputRefusesAnExistingFileInPlaceOfTheDirectory(t *testing.T) {
	dir := t.TempDir() + "/capi-shell-mcp"
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := saveOutput(dir, "secret"); err == nil {
		t.Fatal("saveOutput succeeded although the output directory is a file")
	}
}

func TestExecReadonlyJQThatNeverFinishesIsStoppedAtTheDeadline(t *testing.T) {
	old := jqTimeout
	jqTimeout = 100 * time.Millisecond
	t.Cleanup(func() { jqTimeout = old })

	s, _ := newFakeToolServer(t, "cluster-1")
	s.execer.(*fakeExecer).scriptedStdout = podListJSON
	done := make(chan struct{})
	var results []clusterExecResult
	go func() {
		defer close(done)
		results, _ = readonlyResults(t, s, ReadonlyExecInput{
			Clusters: []string{clusterOneTarget},
			Args:     []string{"get", "pods", "-o", "json"},
			JQ:       "until(false; .)", // emits nothing and never ends: the output cap never trips
		})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a jq expression that never finishes was not stopped")
	}
	if len(results) != 1 || !strings.Contains(results[0].Error, "jq") || !strings.Contains(results[0].Error, "deadline") {
		t.Errorf("results = %+v, want a per-cluster jq timeout error", results)
	}
	if results[0].Stdout != "" {
		t.Errorf("Stdout = %q, want nothing from a stopped expression", results[0].Stdout)
	}
}

func TestAppendJQResultRefusesAResultThatWouldExceedTheCapWithoutWritingIt(t *testing.T) {
	var out strings.Builder
	out.WriteString(strings.Repeat("a", maxJQOutputBytes-10))

	if err := appendJQResult(&out, strings.Repeat("b", 100)); err == nil {
		t.Fatal("appendJQResult accepted a string that pushes the output past the cap")
	}
	if out.Len() != maxJQOutputBytes-10 {
		t.Errorf("output grew to %d bytes although the result was refused", out.Len())
	}

	// A structured value is bounded the same way.
	if err := appendJQResult(&out, map[string]any{"k": strings.Repeat("c", 100)}); err == nil {
		t.Fatal("appendJQResult accepted a value that pushes the output past the cap")
	}
	if out.Len() != maxJQOutputBytes-10 {
		t.Errorf("output grew to %d bytes although the value was refused", out.Len())
	}
}

func TestAppendJQResultAppendsStringsRawAndValuesIndentedEachOnItsOwnLine(t *testing.T) {
	var out strings.Builder
	if err := appendJQResult(&out, "name"); err != nil {
		t.Fatal(err)
	}
	if err := appendJQResult(&out, map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "name\n{\n  \"a\": 1\n}\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}
