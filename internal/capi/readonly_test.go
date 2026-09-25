package capi

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
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

func TestEnsureReadOnlyRBACCreatesSAClusterRoleAndBinding(t *testing.T) {
	client := fake.NewClientset()

	if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1()); err != nil {
		t.Fatalf("EnsureReadOnlyRBAC: %v", err)
	}

	sa, err := client.CoreV1().ServiceAccounts("kube-system").Get(context.Background(), "capi-shell-mcp-read-only", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ServiceAccount: %v", err)
	}
	if sa.Namespace != "kube-system" {
		t.Errorf("SA namespace = %q", sa.Namespace)
	}

	role, err := client.RbacV1().ClusterRoles().Get(context.Background(), "capi-shell-mcp-read-only", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ClusterRole: %v", err)
	}
	if len(role.Rules) != 2 {
		t.Errorf("ClusterRole.Rules = %+v, want 2 rules", role.Rules)
	}

	crb, err := client.RbacV1().ClusterRoleBindings().Get(context.Background(), "capi-shell-mcp-read-only", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ClusterRoleBinding: %v", err)
	}
	if crb.RoleRef.Name != "capi-shell-mcp-read-only" {
		t.Errorf("RoleRef = %+v", crb.RoleRef)
	}
}

func TestEnsureReadOnlyRBACIgnoresExistingSAAndUpdatesExistingClusterRole(t *testing.T) {
	client := fake.NewClientset(
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "capi-shell-mcp-read-only", Namespace: "kube-system"}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "capi-shell-mcp-read-only"}, Rules: []rbacv1.PolicyRule{}},
	)

	if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1()); err != nil {
		t.Fatalf("EnsureReadOnlyRBAC: %v", err)
	}

	role, err := client.RbacV1().ClusterRoles().Get(context.Background(), "capi-shell-mcp-read-only", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ClusterRole: %v", err)
	}
	if len(role.Rules) != 2 {
		t.Errorf("ClusterRole.Rules = %+v, want the replaced 2-rule set", role.Rules)
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

	if err := EnsureReadOnlyRBAC(context.Background(), client.CoreV1(), client.RbacV1()); err != nil {
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

func withTokenReactor(client *fake.Clientset, token string) {
	client.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "token" {
			return false, nil, nil
		}
		return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{Token: token}}, nil
	})
}

func TestCreateReadOnlyKubeconfigReturnsYAMLWithTokenServerAndCA(t *testing.T) {
	client := fake.NewClientset()
	withTokenReactor(client, "mytoken")

	got, err := CreateReadOnlyKubeconfig(context.Background(), client.CoreV1(), client.RbacV1(), adminKcYAMLFixture, 3600)
	if err != nil {
		t.Fatalf("CreateReadOnlyKubeconfig: %v", err)
	}

	var parsed struct {
		Clusters []struct {
			Cluster struct {
				Server string `yaml:"server"`
				CAData string `yaml:"certificate-authority-data"`
			} `yaml:"cluster"`
		} `yaml:"clusters"`
		Users []struct {
			User struct {
				Token string `yaml:"token"`
			} `yaml:"user"`
		} `yaml:"users"`
		CurrentContext string `yaml:"current-context"`
	}
	if err := yaml.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if parsed.Clusters[0].Cluster.Server != "https://10.0.0.1:6443" {
		t.Errorf("server = %q", parsed.Clusters[0].Cluster.Server)
	}
	if parsed.Clusters[0].Cluster.CAData != "dGVzdC1jYQ==" {
		t.Errorf("ca data = %q", parsed.Clusters[0].Cluster.CAData)
	}
	if parsed.Users[0].User.Token != "mytoken" {
		t.Errorf("token = %q", parsed.Users[0].User.Token)
	}
	if parsed.CurrentContext != "readonly" {
		t.Errorf("current-context = %q", parsed.CurrentContext)
	}
}

func TestCreateReadOnlyKubeconfigErrorsWhenTokenRequestReturnsNoToken(t *testing.T) {
	client := fake.NewClientset()
	withTokenReactor(client, "")

	_, err := CreateReadOnlyKubeconfig(context.Background(), client.CoreV1(), client.RbacV1(), adminKcYAMLFixture, 60)
	if err == nil || !strings.Contains(err.Error(), "no token") {
		t.Fatalf("err = %v, want mention of 'no token'", err)
	}
}

func TestCreateReadOnlyKubeconfigErrorsWhenAdminKubeconfigHasNoClusters(t *testing.T) {
	client := fake.NewClientset()
	withTokenReactor(client, "mytoken")

	_, err := CreateReadOnlyKubeconfig(context.Background(), client.CoreV1(), client.RbacV1(), "apiVersion: v1\nkind: Config\nclusters: []\n", 60)
	if err == nil || !strings.Contains(err.Error(), "no cluster") {
		t.Fatalf("err = %v, want mention of 'no cluster'", err)
	}
}
