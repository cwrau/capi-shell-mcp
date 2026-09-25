package capi

import (
	"strings"
	"testing"

	"github.com/cwrau/capi-shell-mcp/internal/config"
	"gopkg.in/yaml.v3"
)

const kubeconfigForTransform = `
clusters:
  - cluster:
      server: https://10.0.0.1:6443
      certificate-authority-data: abc123
    name: my-cluster
users:
  - name: admin
    user:
      client-certificate-data: cert123
      client-key-data: key456
`

func TestApplyKubeconfigTransformOverridesServerURL(t *testing.T) {
	got, err := ApplyKubeconfigTransform(kubeconfigForTransform, config.KubeconfigTransforms{
		Clusters: `.cluster.server = "https://custom.host:6443"`,
	})
	if err != nil {
		t.Fatalf("ApplyKubeconfigTransform: %v", err)
	}
	if !strings.Contains(got, "https://custom.host:6443") {
		t.Errorf("result = %q, want it to contain the overridden server URL", got)
	}
}

func TestApplyKubeconfigTransformReplacesUserCredsWithOIDCExecPlugin(t *testing.T) {
	got, err := ApplyKubeconfigTransform(kubeconfigForTransform, config.KubeconfigTransforms{
		Users: `{name: .name, user: {exec: {apiVersion: "client.authentication.k8s.io/v1beta1", command: "kubectl", args: ["oidc-login", "get-token"]}}}`,
	})
	if err != nil {
		t.Fatalf("ApplyKubeconfigTransform: %v", err)
	}
	var parsed struct {
		Users []struct {
			Name string `yaml:"name"`
			User struct {
				Exec struct {
					Command string   `yaml:"command"`
					Args    []string `yaml:"args"`
				} `yaml:"exec"`
			} `yaml:"user"`
		} `yaml:"users"`
	}
	if err := yaml.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if len(parsed.Users) != 1 || parsed.Users[0].Name != "admin" {
		t.Fatalf("parsed.Users = %+v", parsed.Users)
	}
	if parsed.Users[0].User.Exec.Command != "kubectl" {
		t.Errorf("exec.command = %q, want kubectl", parsed.Users[0].User.Exec.Command)
	}
	if strings.Contains(got, "client-certificate-data") || strings.Contains(got, "client-key-data") {
		t.Errorf("result still contains cert-based credentials: %q", got)
	}
}

func TestApplyKubeconfigTransformIdentityPreservesStructure(t *testing.T) {
	got, err := ApplyKubeconfigTransform(kubeconfigForTransform, config.KubeconfigTransforms{Clusters: "."})
	if err != nil {
		t.Fatalf("ApplyKubeconfigTransform: %v", err)
	}
	var parsed struct {
		Clusters []any `yaml:"clusters"`
	}
	if err := yaml.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if len(parsed.Clusters) != 1 {
		t.Fatalf("len(Clusters) = %d, want 1", len(parsed.Clusters))
	}
}
