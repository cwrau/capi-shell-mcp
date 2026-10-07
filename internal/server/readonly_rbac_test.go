package server

import (
	"context"
	"testing"

	"github.com/cwrau/capi-shell-mcp/internal/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// stubDiscovery is the discovery API of the fake workload cluster.
type stubDiscovery struct{}

func (stubDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	verbs := metav1.Verbs{"get", "list", "watch"}
	return nil, []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "pods", Verbs: verbs},
			{Name: "secrets", Verbs: verbs},
		}},
	}, nil
}

func TestReadOnlyKubeconfigGeneratesTheRoleFromTheConfiguredDenyList(t *testing.T) {
	for name, tc := range map[string]struct {
		denied      []string
		wantSecrets bool
	}{
		"default denies secrets":           {denied: config.DefaultDeniedResources(), wantSecrets: false},
		"configured list is honoured":      {denied: []string{"pods"}, wantSecrets: true},
		"empty list allows everything too": {denied: []string{}, wantSecrets: true},
	} {
		t.Run(name, func(t *testing.T) {
			s, fc := newFakeToolServer(t, "c1")
			s.cfg.ReadOnly.DeniedResources = tc.denied
			if _, err := s.readOnlyKubeconfig(context.Background(), roTestAdminKubeconfig); err != nil {
				t.Fatalf("readOnlyKubeconfig: %v", err)
			}
			role, err := fc.core.RbacV1().ClusterRoles().Get(context.Background(), "capi-shell-mcp-read-only", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("get ClusterRole: %v", err)
			}
			var secrets bool
			for _, rule := range role.Rules {
				for _, r := range rule.Resources {
					if r == "secrets" || r == "*" {
						secrets = true
					}
				}
			}
			if secrets != tc.wantSecrets {
				t.Errorf("secrets readable = %v, want %v (rules %+v)", secrets, tc.wantSecrets, role.Rules)
			}
		})
	}
}

const roTestAdminKubeconfig = `apiVersion: v1
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
