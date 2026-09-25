package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validYAML = `
kubeconfigs:
  prod:
    path: /home/user/.kube/prod.yaml
  dev:
    path: /home/user/.kube/dev.yaml
    contexts:
      - dev-admin
cache:
  cluster_list_ttl: 60
  kubeconfig_ttl: 120
`

const minimalYAML = `
kubeconfigs:
  only:
    path: /tmp/only.yaml
`

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestLoadConfigParsesValidConfigWithTwoKubeconfigs(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, validYAML))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Kubeconfigs) != 2 {
		t.Fatalf("got %d kubeconfigs, want 2", len(cfg.Kubeconfigs))
	}
	prod := cfg.Kubeconfigs["prod"]
	if prod.Name != "prod" || prod.Path != "/home/user/.kube/prod.yaml" || len(prod.Contexts) != 0 {
		t.Fatalf("prod = %+v", prod)
	}
	dev := cfg.Kubeconfigs["dev"]
	if dev.Name != "dev" || dev.Path != "/home/user/.kube/dev.yaml" || len(dev.Contexts) != 1 || dev.Contexts[0] != "dev-admin" {
		t.Fatalf("dev = %+v", dev)
	}
}

func TestLoadConfigParsesGlobalTransforms(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
kubeconfigs:
  prod:
    path: /tmp/kc.yaml
transforms:
  users: '{name: .name, user: {exec: {}}}'
  clusters: '.cluster.server = "https://custom.host:6443"'
`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Transforms == nil {
		t.Fatal("expected non-nil Transforms")
	}
	if cfg.Transforms.Users != `{name: .name, user: {exec: {}}}` {
		t.Errorf("Users = %q", cfg.Transforms.Users)
	}
	if cfg.Transforms.Clusters != `.cluster.server = "https://custom.host:6443"` {
		t.Errorf("Clusters = %q", cfg.Transforms.Clusters)
	}
}

func TestLoadConfigParsesGlobalCustomFields(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
kubeconfigs:
  prod:
    path: /tmp/kc.yaml
custom_fields:
  customer_name: '.metadata.labels["example.com/customer"]'
  tier: '.metadata.labels["example.com/tier"]'
`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	want := map[string]string{
		"customer_name": `.metadata.labels["example.com/customer"]`,
		"tier":          `.metadata.labels["example.com/tier"]`,
	}
	if len(cfg.CustomFields) != len(want) {
		t.Fatalf("CustomFields = %+v, want %+v", cfg.CustomFields, want)
	}
	for k, v := range want {
		if cfg.CustomFields[k] != v {
			t.Errorf("CustomFields[%q] = %q, want %q", k, cfg.CustomFields[k], v)
		}
	}
}

func TestLoadConfigParsesGlobalAndPerKubeconfigSshuttleHost(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
kubeconfigs:
  prod:
    path: /tmp/kc.yaml
    plugins:
      api-endpoint-proxy:
        sshuttle:
          host: user@bastion.example.com
  dev:
    path: /tmp/kc2.yaml
plugins:
  api-endpoint-proxy:
    sshuttle:
      host: gateway
`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SshuttleHost != "gateway" {
		t.Errorf("SshuttleHost = %q, want %q", cfg.SshuttleHost, "gateway")
	}
	if cfg.Kubeconfigs["prod"].SshuttleHost != "user@bastion.example.com" {
		t.Errorf("prod.SshuttleHost = %q", cfg.Kubeconfigs["prod"].SshuttleHost)
	}
	if cfg.Kubeconfigs["dev"].SshuttleHost != "" {
		t.Errorf("dev.SshuttleHost = %q, want empty", cfg.Kubeconfigs["dev"].SshuttleHost)
	}
}

func TestLoadConfigExpandsEnvVarsInPathAndPerKubeconfigSshuttleHost(t *testing.T) {
	t.Setenv("TEST_XDG", "/home/test/.config")
	t.Setenv("TEST_BASTION", "ops@jump.example.com")

	cfg, err := LoadConfig(writeConfig(t, `
kubeconfigs:
  prod:
    path: $TEST_XDG/kube/prod
    plugins:
      api-endpoint-proxy:
        sshuttle:
          host: $TEST_BASTION
`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Kubeconfigs["prod"].Path != "/home/test/.config/kube/prod" {
		t.Errorf("Path = %q", cfg.Kubeconfigs["prod"].Path)
	}
	if cfg.Kubeconfigs["prod"].SshuttleHost != "ops@jump.example.com" {
		t.Errorf("SshuttleHost = %q", cfg.Kubeconfigs["prod"].SshuttleHost)
	}
}

func TestLoadConfigExpandsEnvVarsInGlobalSshuttleHost(t *testing.T) {
	t.Setenv("TEST_GW", "gw.example.com")

	cfg, err := LoadConfig(writeConfig(t, `
kubeconfigs:
  prod:
    path: /tmp/kc.yaml
plugins:
  api-endpoint-proxy:
    sshuttle:
      host: $TEST_GW
`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SshuttleHost != "gw.example.com" {
		t.Errorf("SshuttleHost = %q", cfg.SshuttleHost)
	}
}

func TestLoadConfigExpandsBracedVarSyntax(t *testing.T) {
	t.Setenv("TEST_HOME", "/home/test")

	cfg, err := LoadConfig(writeConfig(t, `
kubeconfigs:
  prod:
    path: ${TEST_HOME}/.kube/prod
`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Kubeconfigs["prod"].Path != "/home/test/.kube/prod" {
		t.Errorf("Path = %q", cfg.Kubeconfigs["prod"].Path)
	}
}

func TestLoadConfigSetsOptionalFieldsToZeroValueWhenAbsent(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, validYAML))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Kubeconfigs["prod"].SshuttleHost != "" {
		t.Errorf("prod.SshuttleHost = %q, want empty", cfg.Kubeconfigs["prod"].SshuttleHost)
	}
	if cfg.Transforms != nil {
		t.Errorf("Transforms = %+v, want nil", cfg.Transforms)
	}
	if cfg.CustomFields != nil {
		t.Errorf("CustomFields = %+v, want nil", cfg.CustomFields)
	}
	if cfg.SshuttleHost != "" {
		t.Errorf("SshuttleHost = %q, want empty", cfg.SshuttleHost)
	}
}

func TestLoadConfigParsesCacheSettings(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, validYAML))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Cache.ClusterListTTLSeconds != 60 {
		t.Errorf("ClusterListTTLSeconds = %d, want 60", cfg.Cache.ClusterListTTLSeconds)
	}
	if cfg.Cache.KubeconfigTTLSeconds != 120 {
		t.Errorf("KubeconfigTTLSeconds = %d, want 120", cfg.Cache.KubeconfigTTLSeconds)
	}
}

func TestLoadConfigUsesDefaultCacheValuesWhenCacheSectionAbsent(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, minimalYAML))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Cache.ClusterListTTLSeconds != 300 {
		t.Errorf("ClusterListTTLSeconds = %d, want 300", cfg.Cache.ClusterListTTLSeconds)
	}
	if cfg.Cache.KubeconfigTTLSeconds != 3600 {
		t.Errorf("KubeconfigTTLSeconds = %d, want 3600", cfg.Cache.KubeconfigTTLSeconds)
	}
}

func TestLoadConfigErrorsWhenKubeconfigsMissing(t *testing.T) {
	_, err := LoadConfig(writeConfig(t, "cache:\n  cluster_list_ttl: 10\n"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if want := "kubeconfigs"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not mention %q", err.Error(), want)
	}
}

func TestLoadConfigDoesNotExpandVarsInTransforms(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
kubeconfigs:
  prod:
    path: /tmp/kc
transforms:
  users: '.name | $__loc__'
`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Transforms.Users != `.name | $__loc__` {
		t.Errorf("Users = %q, want unexpanded jq expression", cfg.Transforms.Users)
	}
}

func TestLoadConfigDoesNotExpandVarsInCustomFields(t *testing.T) {
	t.Setenv("HOME", "/home/should-not-appear")
	cfg, err := LoadConfig(writeConfig(t, `
kubeconfigs:
  prod:
    path: /tmp/kc
custom_fields:
  name: '.metadata.labels["$HOME"]'
`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.CustomFields["name"] != `.metadata.labels["$HOME"]` {
		t.Errorf("CustomFields[name] = %q, want unexpanded jq expression", cfg.CustomFields["name"])
	}
}

func TestLoadUsesEnvVarForConfigPath(t *testing.T) {
	path := writeConfig(t, minimalYAML)
	t.Setenv("CAPI_SHELL_MCP_CONFIG", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Kubeconfigs) != 1 {
		t.Fatalf("got %d kubeconfigs, want 1", len(cfg.Kubeconfigs))
	}
}
