// Package capi discovers CAPI (Cluster API) workload clusters across
// configured kubeconfigs and produces their kubeconfigs, OpenStack
// credentials, and read-only tokens.
package capi

import (
	"slices"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// GetContexts returns the names of every context defined in kubeconfigYAML,
// sorted for determinism (kubeconfig contexts have no inherent order).
func GetContexts(kubeconfigYAML []byte) ([]string, error) {
	raw, err := clientcmd.Load(kubeconfigYAML)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(raw.Contexts))
	for name := range raw.Contexts {
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

// RESTConfigForContext builds a *rest.Config from kubeconfigYAML, using
// context if non-empty, or the kubeconfig's current-context otherwise.
func RESTConfigForContext(kubeconfigYAML []byte, context string) (*rest.Config, error) {
	raw, err := clientcmd.Load(kubeconfigYAML)
	if err != nil {
		return nil, err
	}
	clientConfig := clientcmd.NewNonInteractiveClientConfig(*raw, context, &clientcmd.ConfigOverrides{}, nil)
	return clientConfig.ClientConfig()
}
