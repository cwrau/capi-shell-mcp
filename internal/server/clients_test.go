package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cwrau/capi-shell-mcp/internal/config"
)

const testKubeconfig = `
apiVersion: v1
kind: Config
clusters:
- name: mgmt
  cluster:
    server: https://mgmt.example.com:6443
contexts:
- name: mgmt-admin
  context:
    cluster: mgmt
    user: admin
current-context: mgmt-admin
users:
- name: admin
  user:
    token: sometoken
`

func TestRealClientsForContextBuildsClientsFromKubeconfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kc.yaml")
	if err := os.WriteFile(path, []byte(testKubeconfig), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	src := config.KubeconfigSource{Name: "prod", Path: path}

	clients, err := RealClientsForContext(src, "mgmt-admin")
	if err != nil {
		t.Fatalf("RealClientsForContext: %v", err)
	}
	if clients.Dynamic == nil || clients.Core == nil || clients.Rbac == nil {
		t.Fatalf("clients = %+v, want all non-nil", clients)
	}
}

func TestClientsFromKubeconfigYAMLBuildsClients(t *testing.T) {
	core, rbac, err := ClientsFromKubeconfigYAML(testKubeconfig)
	if err != nil {
		t.Fatalf("ClientsFromKubeconfigYAML: %v", err)
	}
	if core == nil || rbac == nil {
		t.Fatal("expected non-nil core and rbac clients")
	}
}
