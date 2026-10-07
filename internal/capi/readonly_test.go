package capi

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const adminKcYAMLFixture = `
apiVersion: v1
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

// fakeLister stands in for a cluster's discovery API.
type fakeLister struct {
	lists []*metav1.APIResourceList
	err   error
	calls int
}

func (f *fakeLister) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	f.calls++
	return nil, f.lists, f.err
}

var (
	readVerbs  = metav1.Verbs{"get", "list", "watch"}
	allVerbs   = metav1.Verbs{"create", "delete", "get", "list", "patch", "update", "watch"}
	writeVerbs = metav1.Verbs{"create"}
)

// discoveryFixture is a small cluster: the core group (including
// subresources that must stay out), apps, a CRD group, a group that only
// offers a write verb, and a second "events" resource in another group.
func discoveryFixture() *fakeLister {
	return &fakeLister{lists: []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "pods", Verbs: allVerbs},
			{Name: "pods/log", Verbs: metav1.Verbs{"get"}},
			{Name: "pods/status", Verbs: metav1.Verbs{"get", "patch", "update"}},
			{Name: "pods/exec", Verbs: metav1.Verbs{"create", "get"}},
			{Name: "pods/portforward", Verbs: metav1.Verbs{"create", "get"}},
			{Name: "pods/eviction", Verbs: writeVerbs},
			{Name: "secrets", Verbs: allVerbs},
			{Name: "nodes", Verbs: allVerbs},
			{Name: "nodes/proxy", Verbs: metav1.Verbs{"create", "get"}},
			{Name: "namespaces", Verbs: allVerbs},
			{Name: "events", Verbs: allVerbs},
		}},
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
			{Name: "deployments", Verbs: allVerbs},
			{Name: "deployments/scale", Verbs: metav1.Verbs{"get", "patch", "update"}},
			{Name: "deployments/status", Verbs: metav1.Verbs{"get", "patch", "update"}},
		}},
		{GroupVersion: "apps/v1beta1", APIResources: []metav1.APIResource{
			{Name: "deployments", Verbs: allVerbs},
		}},
		{GroupVersion: "events.k8s.io/v1", APIResources: []metav1.APIResource{
			{Name: "events", Verbs: allVerbs},
		}},
		{GroupVersion: "cilium.io/v2", APIResources: []metav1.APIResource{
			{Name: "ciliumnetworkpolicies", Verbs: allVerbs},
			{Name: "ciliumnetworkpolicies/status", Verbs: metav1.Verbs{"get", "patch", "update"}},
		}},
		{GroupVersion: "authentication.k8s.io/v1", APIResources: []metav1.APIResource{
			{Name: "tokenreviews", Verbs: writeVerbs},
		}},
	}}
}

func getReadOnlyRole(t *testing.T, client *fake.Clientset) *rbacv1.ClusterRole {
	t.Helper()
	role, err := client.RbacV1().ClusterRoles().Get(context.Background(), "capi-shell-mcp-read-only", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ClusterRole: %v", err)
	}
	return role
}

// readableByGroup flattens a role into group -> resource set, failing the
// test on anything but plain get/list/watch rules.
func readableByGroup(t *testing.T, role *rbacv1.ClusterRole) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	for _, rule := range role.Rules {
		if len(rule.NonResourceURLs) > 0 {
			continue
		}
		if got := strings.Join(rule.Verbs, ","); got != "get,list,watch" {
			t.Errorf("rule %+v has verbs %s, want get,list,watch", rule, got)
		}
		for _, g := range rule.APIGroups {
			if out[g] == nil {
				out[g] = map[string]bool{}
			}
			for _, r := range rule.Resources {
				out[g][r] = true
			}
		}
	}
	return out
}

func TestEnsureReadOnlyRBACCreatesSAGeneratedRoleAndBinding(t *testing.T) {
	client := fake.NewClientset()

	if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), discoveryFixture(), []string{"secrets"}); err != nil {
		t.Fatalf("EnsureReadOnlyRBAC: %v", err)
	}

	if _, err := client.CoreV1().ServiceAccounts("kube-system").Get(context.Background(), "capi-shell-mcp-read-only", metav1.GetOptions{}); err != nil {
		t.Fatalf("get ServiceAccount: %v", err)
	}

	crb, err := client.RbacV1().ClusterRoleBindings().Get(context.Background(), "capi-shell-mcp-read-only", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ClusterRoleBinding: %v", err)
	}
	if crb.RoleRef.Kind != "ClusterRole" || crb.RoleRef.Name != "capi-shell-mcp-read-only" {
		t.Errorf("RoleRef = %+v, want our generated ClusterRole", crb.RoleRef)
	}
	if len(crb.Subjects) != 1 || crb.Subjects[0].Kind != "ServiceAccount" || crb.Subjects[0].Name != "capi-shell-mcp-read-only" || crb.Subjects[0].Namespace != "kube-system" {
		t.Errorf("Subjects = %+v", crb.Subjects)
	}
	bindings, err := client.RbacV1().ClusterRoleBindings().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list ClusterRoleBindings: %v", err)
	}
	if len(bindings.Items) != 1 {
		t.Errorf("ClusterRoleBindings = %+v, want exactly one", bindings.Items)
	}
}

func TestEnsureReadOnlyRBACGeneratedRoleReadsEverythingButDeniedResources(t *testing.T) {
	client := fake.NewClientset()
	if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), discoveryFixture(), []string{"secrets"}); err != nil {
		t.Fatalf("EnsureReadOnlyRBAC: %v", err)
	}
	role := getReadOnlyRole(t, client)
	got := readableByGroup(t, role)

	for group, wants := range map[string][]string{
		"":              {"pods", "pods/log", "pods/status", "nodes", "namespaces", "events"},
		"apps":          {"deployments", "deployments/scale", "deployments/status"},
		"events.k8s.io": {"events"},
		"cilium.io":     {"ciliumnetworkpolicies", "ciliumnetworkpolicies/status"},
	} {
		for _, r := range wants {
			if !got[group][r] {
				t.Errorf("group %q resource %q is not readable; rules = %+v", group, r, role.Rules)
			}
		}
	}

	for group, nots := range map[string][]string{
		"": {"secrets", "pods/exec", "pods/portforward", "pods/eviction", "nodes/proxy"},
	} {
		for _, r := range nots {
			if got[group][r] {
				t.Errorf("group %q resource %q must not be readable", group, r)
			}
		}
	}
	if _, ok := got["authentication.k8s.io"]; ok {
		t.Errorf("a group offering no read verb must not appear: %+v", got["authentication.k8s.io"])
	}

	// No wildcards in the generated role's resource rules, and the
	// non-resource URLs stay readable.
	var nonResource bool
	for _, rule := range role.Rules {
		for _, s := range append(append([]string{}, rule.APIGroups...), rule.Resources...) {
			if s == "*" {
				t.Errorf("unexpected wildcard in %+v", rule)
			}
		}
		if len(rule.NonResourceURLs) > 0 {
			nonResource = true
			if strings.Join(rule.Verbs, ",") != "get" {
				t.Errorf("non-resource rule verbs = %v, want get", rule.Verbs)
			}
		}
	}
	if !nonResource {
		t.Error("no non-resource URL rule")
	}
}

func TestEnsureReadOnlyRBACDeniedResourceWithGroupOnlyAffectsThatGroup(t *testing.T) {
	client := fake.NewClientset()
	if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), discoveryFixture(), []string{"apps/deployments", "events"}); err != nil {
		t.Fatalf("EnsureReadOnlyRBAC: %v", err)
	}
	got := readableByGroup(t, getReadOnlyRole(t, client))

	// "deployments.apps" and everything under it is gone, the rest of apps is not.
	for _, r := range []string{"deployments", "deployments/scale", "deployments/status"} {
		if got["apps"][r] {
			t.Errorf("apps/%s must be denied", r)
		}
	}
	// "events" is the core resource only, events.k8s.io keeps its own.
	if got[""]["events"] {
		t.Error("core events must be denied")
	}
	if !got["events.k8s.io"]["events"] {
		t.Error("events.k8s.io events must stay readable")
	}
	// Secrets are no longer in the deny list here, so they are readable.
	if !got[""]["secrets"] {
		t.Error("secrets must be readable when they are not denied")
	}
}

func TestEnsureReadOnlyRBACDeniedSubresourceParentDeniesItsSubresources(t *testing.T) {
	client := fake.NewClientset()
	if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), discoveryFixture(), []string{"pods"}); err != nil {
		t.Fatalf("EnsureReadOnlyRBAC: %v", err)
	}
	got := readableByGroup(t, getReadOnlyRole(t, client))
	for _, r := range []string{"pods", "pods/log", "pods/status"} {
		if got[""][r] {
			t.Errorf("%s must be denied together with pods", r)
		}
	}
}

func TestEnsureReadOnlyRBACEmptyDenyListGrantsEveryReadableResourceButNoWildcard(t *testing.T) {
	client := fake.NewClientset()

	if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), discoveryFixture(), nil); err != nil {
		t.Fatalf("EnsureReadOnlyRBAC: %v", err)
	}
	role := getReadOnlyRole(t, client)
	got := readableByGroup(t, role)

	if !got[""]["secrets"] || !got[""]["pods"] || !got["apps"]["deployments"] {
		t.Errorf("nothing is denied, so everything readable must be granted, Secrets included: %+v", got)
	}
	for _, r := range []string{"pods/exec", "pods/portforward", "nodes/proxy"} {
		if got[""][r] {
			t.Errorf("%s answers GET but is no read and must stay out even with an empty deny list", r)
		}
	}
	for _, rule := range role.Rules {
		for _, s := range append(append([]string{}, rule.APIGroups...), rule.Resources...) {
			if s == "*" {
				t.Errorf("unexpected wildcard in %+v", rule)
			}
		}
	}
}

func TestEnsureReadOnlyRBACFailsClosedWhenDiscoveryFails(t *testing.T) {
	client := fake.NewClientset()
	lister := &fakeLister{err: errors.New("apiserver unreachable")}

	err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), lister, []string{"secrets"})
	if err == nil || !strings.Contains(err.Error(), "apiserver unreachable") {
		t.Fatalf("err = %v, want the discovery error", err)
	}
	if roles, _ := client.RbacV1().ClusterRoles().List(context.Background(), metav1.ListOptions{}); len(roles.Items) != 0 {
		t.Errorf("ClusterRoles = %+v, want none", roles.Items)
	}
	if bindings, _ := client.RbacV1().ClusterRoleBindings().List(context.Background(), metav1.ListOptions{}); len(bindings.Items) != 0 {
		t.Errorf("ClusterRoleBindings = %+v, want none", bindings.Items)
	}
}

func TestEnsureReadOnlyRBACFailsClosedWhenDiscoveryIsEmpty(t *testing.T) {
	client := fake.NewClientset()
	err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), &fakeLister{}, []string{"secrets"})
	if err == nil {
		t.Fatal("EnsureReadOnlyRBAC succeeded with no discovered resources, want an error")
	}
}

func TestEnsureReadOnlyRBACKeepsGoingOnPartialDiscoveryFailure(t *testing.T) {
	client := fake.NewClientset()
	lister := discoveryFixture()
	lister.err = &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
		{Group: "metrics.k8s.io", Version: "v1beta1"}: errors.New("the server is currently unable to handle the request"),
	}}

	if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), lister, []string{"secrets"}); err != nil {
		t.Fatalf("EnsureReadOnlyRBAC: %v", err)
	}
	if !readableByGroup(t, getReadOnlyRole(t, client))[""]["pods"] {
		t.Error("the groups that were discovered must still be granted")
	}
}

func TestEnsureReadOnlyRBACUpdatesTheRoleOnlyWhenItChanged(t *testing.T) {
	client := fake.NewClientset(&rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "capi-shell-mcp-read-only"},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"get"}}},
	})
	var updates int
	client.PrependReactor("update", "clusterroles", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		return false, nil, nil
	})

	for i := 0; i < 3; i++ {
		if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), discoveryFixture(), []string{"secrets"}); err != nil {
			t.Fatalf("EnsureReadOnlyRBAC #%d: %v", i, err)
		}
	}
	if updates != 1 {
		t.Errorf("ClusterRole updates = %d, want 1 (stale role fixed once, then left alone)", updates)
	}
	if readableByGroup(t, getReadOnlyRole(t, client))[""]["secrets"] {
		t.Error("stale wildcard rules must be replaced")
	}
}

func TestEnsureReadOnlyRBACIgnoresExistingSA(t *testing.T) {
	client := fake.NewClientset(
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "capi-shell-mcp-read-only", Namespace: "kube-system"}},
	)

	if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), discoveryFixture(), []string{"secrets"}); err != nil {
		t.Fatalf("EnsureReadOnlyRBAC: %v", err)
	}
}

func TestEnsureReadOnlyRBACRemovesTheBindingToTheBuiltinViewRole(t *testing.T) {
	client := fake.NewClientset(&rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "capi-shell-mcp-read-only-view"},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "view"},
	})

	if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), discoveryFixture(), []string{"secrets"}); err != nil {
		t.Fatalf("EnsureReadOnlyRBAC: %v", err)
	}
	if _, err := client.RbacV1().ClusterRoleBindings().Get(context.Background(), "capi-shell-mcp-read-only-view", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("view binding still present (err = %v)", err)
	}
	if _, err := client.RbacV1().ClusterRoleBindings().Get(context.Background(), "capi-shell-mcp-read-only", metav1.GetOptions{}); err != nil {
		t.Errorf("generated binding missing: %v", err)
	}
}

func TestEnsureReadOnlyRBACFallsBackToDeleteRecreateOnImmutableRoleRef(t *testing.T) {
	client := fake.NewClientset(
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "capi-shell-mcp-read-only"},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "some-other-role"},
		},
	)

	var updateAttempts int
	client.PrependReactor("update", "clusterrolebindings", func(action k8stesting.Action) (bool, runtime.Object, error) {
		updateAttempts++
		errs := field.ErrorList{field.Forbidden(field.NewPath("roleRef"), "roleRef is immutable")}
		return true, nil, apierrors.NewInvalid(schema.GroupKind{Group: "rbac.authorization.k8s.io", Kind: "ClusterRoleBinding"}, "capi-shell-mcp-read-only", errs)
	})

	if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), discoveryFixture(), []string{"secrets"}); err != nil {
		t.Fatalf("EnsureReadOnlyRBAC: %v", err)
	}
	if updateAttempts != 1 {
		t.Errorf("update attempts = %d, want 1", updateAttempts)
	}

	crb, err := client.RbacV1().ClusterRoleBindings().Get(context.Background(), "capi-shell-mcp-read-only", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ClusterRoleBinding: %v", err)
	}
	if crb.RoleRef.Name != "capi-shell-mcp-read-only" {
		t.Errorf("RoleRef.Name = %q, want the recreated binding's roleRef", crb.RoleRef.Name)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEnsureReadOnlyRBACSurvivesAnotherCallCreatingTheRoleFirst(t *testing.T) {
	// A concurrent call already created the role with other rules, after our
	// Get said NotFound: our Create hits AlreadyExists and must start over.
	client := fake.NewClientset(&rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "capi-shell-mcp-read-only"},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}},
	})
	var gets int
	client.PrependReactor("get", "clusterroles", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets == 1 {
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "clusterroles"}, "capi-shell-mcp-read-only")
		}
		return false, nil, nil
	})

	if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), discoveryFixture(), []string{"secrets"}); err != nil {
		t.Fatalf("EnsureReadOnlyRBAC: %v", err)
	}
	if !readableByGroup(t, getReadOnlyRole(t, client))["apps"]["deployments"] {
		t.Error("the role must end up with the generated rules")
	}
}

func TestEnsureReadOnlyRBACRetriesAnUpdateConflict(t *testing.T) {
	client := fake.NewClientset(&rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "capi-shell-mcp-read-only"},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}},
	})
	var updates int
	client.PrependReactor("update", "clusterroles", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		if updates == 1 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "clusterroles"}, "capi-shell-mcp-read-only", errors.New("the object has been modified"))
		}
		return false, nil, nil
	})

	if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), discoveryFixture(), []string{"secrets"}); err != nil {
		t.Fatalf("EnsureReadOnlyRBAC: %v", err)
	}
	if updates != 2 {
		t.Errorf("update attempts = %d, want 2", updates)
	}
}

func TestEnsureReadOnlyRBACDenyEntryNotationVariantsAllDenyTheResource(t *testing.T) {
	for name, tc := range map[string]struct {
		denied []string
		gone   [][2]string // group, resource that must not be readable
	}{
		"core with version":                  {denied: []string{"v1/secrets"}, gone: [][2]string{{"", "secrets"}}},
		"core written as an empty group":     {denied: []string{"/secrets"}, gone: [][2]string{{"", "secrets"}}},
		"mixed case":                         {denied: []string{"Secrets"}, gone: [][2]string{{"", "secrets"}}},
		"group with version":                 {denied: []string{"apps/v1/deployments"}, gone: [][2]string{{"apps", "deployments"}}},
		"group without version":              {denied: []string{"apps/deployments"}, gone: [][2]string{{"apps", "deployments"}}},
		"version the cluster does not serve": {denied: []string{"apps/v2/deployments", "v1beta1/secrets"}, gone: [][2]string{{"apps", "deployments"}, {"", "secrets"}}},
	} {
		t.Run(name, func(t *testing.T) {
			client := fake.NewClientset()
			if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), discoveryFixture(), tc.denied); err != nil {
				t.Fatalf("EnsureReadOnlyRBAC: %v", err)
			}
			got := readableByGroup(t, getReadOnlyRole(t, client))
			for _, gr := range tc.gone {
				if got[gr[0]][gr[1]] {
					t.Errorf("%s/%s is readable although %v denies it", gr[0], gr[1], tc.denied)
				}
			}
		})
	}
}

func TestEnsureReadOnlyRBACDenyEntryDoesNotReachOtherGroups(t *testing.T) {
	client := fake.NewClientset()
	// events.k8s.io must keep its own "events".
	if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1(), discoveryFixture(), []string{"v1/events", "other.example.com/secrets"}); err != nil {
		t.Fatalf("EnsureReadOnlyRBAC: %v", err)
	}
	got := readableByGroup(t, getReadOnlyRole(t, client))
	if got[""]["events"] {
		t.Error("core events must be denied by events.v1")
	}
	if !got["events.k8s.io"]["events"] {
		t.Error("events.k8s.io events must stay readable")
	}
	if !got[""]["secrets"] {
		t.Error("other.example.com/secrets names a group the cluster does not have, so it denies nothing")
	}
}
