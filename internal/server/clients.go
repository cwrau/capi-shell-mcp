package server

import (
	"fmt"
	"os"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
	"github.com/cwrau/capi-shell-mcp/internal/config"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	rbacv1client "k8s.io/client-go/kubernetes/typed/rbac/v1"
)

// Clients are the Kubernetes clients needed to operate against one
// kubeconfig entry's context.
type Clients struct {
	Dynamic dynamic.Interface
	Core    corev1client.CoreV1Interface
	Rbac    rbacv1client.RbacV1Interface
}

// ClientsForContextFunc resolves the clients for one (kubeconfig entry,
// context) pair. Production wiring uses RealClientsForContext; tests
// inject fakes.
type ClientsForContextFunc func(src config.KubeconfigSource, context string) (*Clients, error)

// RealClientsForContext builds real clients from a kubeconfig entry's
// file, scoped to context (or its current-context if empty).
func RealClientsForContext(src config.KubeconfigSource, context string) (*Clients, error) {
	data, err := os.ReadFile(src.Path)
	if err != nil {
		return nil, fmt.Errorf("server: reading kubeconfig %s: %w", src.Path, err)
	}
	return clientsFromKubeconfigBytes(data, context)
}

// ClientsFromKubeconfigYAML builds Core/Rbac clients directly from a
// kubeconfig YAML string (its current-context) — used to mint a read-only
// token against the *workload* cluster itself, via its own admin
// kubeconfig, rather than against a configured kubeconfig entry.
func ClientsFromKubeconfigYAML(kubeconfigYAML string) (corev1client.CoreV1Interface, rbacv1client.RbacV1Interface, error) {
	clients, err := clientsFromKubeconfigBytes([]byte(kubeconfigYAML), "")
	if err != nil {
		return nil, nil, err
	}
	return clients.Core, clients.Rbac, nil
}

func clientsFromKubeconfigBytes(data []byte, context string) (*Clients, error) {
	restCfg, err := capi.RESTConfigForContext(data, context)
	if err != nil {
		return nil, fmt.Errorf("server: building rest config: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("server: building dynamic client: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("server: building typed client: %w", err)
	}
	return &Clients{Dynamic: dyn, Core: clientset.CoreV1(), Rbac: clientset.RbacV1()}, nil
}
