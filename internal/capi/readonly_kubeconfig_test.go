package capi

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type readOnlyKubeconfigDoc struct {
	Clusters []struct {
		Cluster struct {
			Server string `yaml:"server"`
			CAData string `yaml:"certificate-authority-data"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			Token        string         `yaml:"token"`
			As           string         `yaml:"as"`
			Exec         map[string]any `yaml:"exec"`
			AuthProvider map[string]any `yaml:"auth-provider"`
		} `yaml:"user"`
	} `yaml:"users"`
	CurrentContext string `yaml:"current-context"`
}

func readOnlyKubeconfigFor(t *testing.T, adminKC string) (readOnlyKubeconfigDoc, string, error) {
	t.Helper()
	client := fake.NewClientset()
	got, err := ReadOnlyKubeconfig(context.Background(), client.CoreV1(), client.RbacV1(), discoveryFixture(), []string{"secrets"}, adminKC)
	if err != nil {
		return readOnlyKubeconfigDoc{}, got, err
	}
	var doc readOnlyKubeconfigDoc
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	return doc, got, nil
}

func TestReadOnlyKubeconfigImpersonatesServiceAccountKeepingStaticCredentials(t *testing.T) {
	doc, _, err := readOnlyKubeconfigFor(t, adminKcYAMLFixture)
	if err != nil {
		t.Fatalf("ReadOnlyKubeconfig: %v", err)
	}
	if doc.Clusters[0].Cluster.Server != "https://10.0.0.1:6443" || doc.Clusters[0].Cluster.CAData != "dGVzdC1jYQ==" {
		t.Errorf("cluster = %+v, want server and CA kept", doc.Clusters[0].Cluster)
	}
	if doc.Users[0].User.Token != "old-admin-token" {
		t.Errorf("token = %q, want the caller's own credential kept so the audit log attributes the request to them", doc.Users[0].User.Token)
	}
	if want := "system:serviceaccount:kube-system:capi-shell-mcp-read-only"; doc.Users[0].User.As != want {
		t.Errorf("as = %q, want %q", doc.Users[0].User.As, want)
	}
	if doc.CurrentContext != "admin" {
		t.Errorf("current-context = %q, want the original kept", doc.CurrentContext)
	}
}

func TestReadOnlyKubeconfigReplacesPreexistingImpersonation(t *testing.T) {
	kc := strings.Replace(adminKcYAMLFixture, "    token: old-admin-token\n", "    token: old-admin-token\n    as: someone-else\n    as-groups:\n    - system:masters\n", 1)
	_, got, err := readOnlyKubeconfigFor(t, kc)
	if err != nil {
		t.Fatalf("ReadOnlyKubeconfig: %v", err)
	}
	if strings.Contains(got, "someone-else") || strings.Contains(got, "system:masters") {
		t.Errorf("kubeconfig still carries the preexisting impersonation:\n%s", got)
	}
}

func TestReadOnlyKubeconfigKeepsOnlyTheCurrentUser(t *testing.T) {
	kc := adminKcYAMLFixture + "- name: other\n  user:\n    token: other-token\n"
	doc, got, err := readOnlyKubeconfigFor(t, kc)
	if err != nil {
		t.Fatalf("ReadOnlyKubeconfig: %v", err)
	}
	if len(doc.Users) != 1 || doc.Users[0].Name != "admin" || strings.Contains(got, "other-token") {
		t.Errorf("users = %+v, want only the current user\n%s", doc.Users, got)
	}
}

func TestReadOnlyKubeconfigEnsuresRBAC(t *testing.T) {
	client := fake.NewClientset()
	if _, err := ReadOnlyKubeconfig(context.Background(), client.CoreV1(), client.RbacV1(), discoveryFixture(), []string{"secrets"}, adminKcYAMLFixture); err != nil {
		t.Fatalf("ReadOnlyKubeconfig: %v", err)
	}
	if _, err := client.CoreV1().ServiceAccounts("kube-system").Get(context.Background(), "capi-shell-mcp-read-only", metav1.GetOptions{}); err != nil {
		t.Errorf("get ServiceAccount: %v", err)
	}
}

func TestReadOnlyKubeconfigErrorsWithoutACurrentUser(t *testing.T) {
	for name, kc := range map[string]string{
		"no users":            "apiVersion: v1\nkind: Config\nusers: []\n",
		"current user absent": strings.Replace(adminKcYAMLFixture, "- name: admin\n  user:", "- name: someone-else\n  user:", 1),
	} {
		if _, _, err := readOnlyKubeconfigFor(t, kc); err == nil || !strings.Contains(err.Error(), "user") {
			t.Errorf("%s: err = %v, want an error about the user", name, err)
		}
	}
}

// execPluginKubeconfig returns an admin kubeconfig whose user authenticates
// through a credential plugin.
func execPluginKubeconfig() string {
	return `
apiVersion: v1
kind: Config
clusters:
- name: workload
  cluster:
    server: https://10.0.0.1:6443
contexts:
- name: oidc
  context:
    cluster: workload
    user: oidc
current-context: oidc
users:
- name: oidc
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: kubectl
      args:
      - oidc-login
      - get-token
      interactiveMode: Never
`
}

func TestReadOnlyKubeconfigKeepsTheCredentialPlugin(t *testing.T) {
	kc := execPluginKubeconfig()

	doc, _, err := readOnlyKubeconfigFor(t, kc)
	if err != nil {
		t.Fatalf("ReadOnlyKubeconfig: %v", err)
	}
	if doc.Users[0].User.Exec == nil || doc.Users[0].User.Token != "" {
		t.Errorf("user = %+v, want the credential plugin kept, not replaced by a token", doc.Users[0].User)
	}
	if doc.Users[0].User.As != "system:serviceaccount:kube-system:capi-shell-mcp-read-only" {
		t.Errorf("as = %q", doc.Users[0].User.As)
	}
}
