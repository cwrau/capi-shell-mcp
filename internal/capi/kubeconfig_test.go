package capi

import "testing"

const twoContextKubeconfig = `
apiVersion: v1
kind: Config
clusters:
- name: cluster-a
  cluster:
    server: https://a.example.com:6443
- name: cluster-b
  cluster:
    server: https://b.example.com:6443
contexts:
- name: ctx-a
  context:
    cluster: cluster-a
    user: admin
- name: ctx-b
  context:
    cluster: cluster-b
    user: admin
current-context: ctx-a
users:
- name: admin
  user:
    token: sometoken
`

func TestGetContextsReturnsContextNames(t *testing.T) {
	got, err := GetContexts([]byte(twoContextKubeconfig))
	if err != nil {
		t.Fatalf("GetContexts: %v", err)
	}
	if len(got) != 2 || got[0] != "ctx-a" || got[1] != "ctx-b" {
		t.Fatalf("got %v, want [ctx-a ctx-b]", got)
	}
}

func TestRESTConfigForContextUsesExplicitContext(t *testing.T) {
	restCfg, err := RESTConfigForContext([]byte(twoContextKubeconfig), "ctx-b")
	if err != nil {
		t.Fatalf("RESTConfigForContext: %v", err)
	}
	if restCfg.Host != "https://b.example.com:6443" {
		t.Errorf("Host = %q, want %q", restCfg.Host, "https://b.example.com:6443")
	}
}

func TestRESTConfigForContextDefaultsToCurrentContext(t *testing.T) {
	restCfg, err := RESTConfigForContext([]byte(twoContextKubeconfig), "")
	if err != nil {
		t.Fatalf("RESTConfigForContext: %v", err)
	}
	if restCfg.Host != "https://a.example.com:6443" {
		t.Errorf("Host = %q, want %q", restCfg.Host, "https://a.example.com:6443")
	}
}
