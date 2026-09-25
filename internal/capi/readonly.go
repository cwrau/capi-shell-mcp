package capi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	rbacv1client "k8s.io/client-go/kubernetes/typed/rbac/v1"
)

const (
	readOnlySAName    = "capi-shell-mcp-read-only"
	readOnlyNamespace = "kube-system"
)

var readOnlyClusterRole = &rbacv1.ClusterRole{
	ObjectMeta: metav1.ObjectMeta{Name: readOnlySAName},
	Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"get", "list", "watch"}},
		{NonResourceURLs: []string{"*"}, Verbs: []string{"get"}},
	},
}

var readOnlyClusterRoleBinding = &rbacv1.ClusterRoleBinding{
	ObjectMeta: metav1.ObjectMeta{Name: readOnlySAName},
	RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: readOnlySAName},
	Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: readOnlySAName, Namespace: readOnlyNamespace}},
}

func isImmutableRoleRefError(err error) bool {
	return apierrors.IsInvalid(err) && strings.Contains(err.Error(), "roleRef") && strings.Contains(err.Error(), "immutable")
}

// EnsureReadOnlyRBAC idempotently ensures the ServiceAccount, ClusterRole,
// and ClusterRoleBinding backing the read-only kubeconfig exist and are
// up to date, recovering from the ClusterRoleBinding's immutable roleRef
// by deleting and recreating it when it has changed.
func EnsureReadOnlyRBAC(ctx context.Context, core corev1client.CoreV1Interface, rbac rbacv1client.RbacV1Interface) error {
	_, err := core.ServiceAccounts(readOnlyNamespace).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: readOnlySAName, Namespace: readOnlyNamespace},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("capi: creating read-only ServiceAccount: %w", err)
	}

	if _, err := rbac.ClusterRoles().Create(ctx, readOnlyClusterRole, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("capi: creating read-only ClusterRole: %w", err)
		}
		if _, err := rbac.ClusterRoles().Update(ctx, readOnlyClusterRole, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("capi: replacing read-only ClusterRole: %w", err)
		}
	}

	_, err = rbac.ClusterRoleBindings().Create(ctx, readOnlyClusterRoleBinding, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("capi: creating read-only ClusterRoleBinding: %w", err)
	}

	if _, err := rbac.ClusterRoleBindings().Update(ctx, readOnlyClusterRoleBinding, metav1.UpdateOptions{}); err != nil {
		if !isImmutableRoleRefError(err) {
			return fmt.Errorf("capi: replacing read-only ClusterRoleBinding: %w", err)
		}
		if err := rbac.ClusterRoleBindings().Delete(ctx, readOnlySAName, metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("capi: deleting read-only ClusterRoleBinding for recreate: %w", err)
		}
		if _, err := rbac.ClusterRoleBindings().Create(ctx, readOnlyClusterRoleBinding, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("capi: recreating read-only ClusterRoleBinding: %w", err)
		}
	}
	return nil
}

type adminKubeconfigClusters struct {
	Clusters []struct {
		Cluster struct {
			Server                   string `yaml:"server"`
			CertificateAuthorityData string `yaml:"certificate-authority-data"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
}

// CreateReadOnlyKubeconfig ensures the read-only RBAC exists, mints a
// fresh ServiceAccount token valid for durationSeconds, and returns a
// minimal kubeconfig YAML using the admin kubeconfig's server/CA data with
// that token. It never reads from a cache: callers get a fresh RBAC-ensure
// and TokenRequest on every call, by design.
func CreateReadOnlyKubeconfig(ctx context.Context, core corev1client.CoreV1Interface, rbac rbacv1client.RbacV1Interface, adminKcYAML string, durationSeconds int64) (string, error) {
	var parsed adminKubeconfigClusters
	if err := yaml.Unmarshal([]byte(adminKcYAML), &parsed); err != nil {
		return "", fmt.Errorf("capi: parsing admin kubeconfig: %w", err)
	}
	if len(parsed.Clusters) == 0 {
		return "", errors.New("createReadOnlyKubeconfig: no cluster found in admin kubeconfig")
	}
	clusterInfo := parsed.Clusters[0].Cluster

	if err := EnsureReadOnlyRBAC(ctx, core, rbac); err != nil {
		return "", err
	}

	tokenReq, err := core.ServiceAccounts(readOnlyNamespace).CreateToken(ctx, readOnlySAName, &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &durationSeconds},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("capi: requesting read-only token: %w", err)
	}
	token := strings.TrimSpace(tokenReq.Status.Token)
	if token == "" {
		return "", errors.New("createReadOnlyKubeconfig: token request returned no token")
	}

	out := map[string]any{
		"apiVersion": "v1",
		"kind":       "Config",
		"clusters": []any{map[string]any{
			"name": "workload",
			"cluster": map[string]any{
				"server":                     clusterInfo.Server,
				"certificate-authority-data": clusterInfo.CertificateAuthorityData,
			},
		}},
		"contexts": []any{map[string]any{
			"name": "readonly",
			"context": map[string]any{
				"cluster":   "workload",
				"user":      readOnlySAName,
				"namespace": readOnlyNamespace,
			},
		}},
		"current-context": "readonly",
		"users": []any{map[string]any{
			"name": readOnlySAName,
			"user": map[string]any{"token": token},
		}},
	}
	b, err := yaml.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("capi: rendering read-only kubeconfig: %w", err)
	}
	return string(b), nil
}
