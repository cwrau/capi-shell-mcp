package capi

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

// FetchWorkloadKubeconfig reads the CAPI-generated "<clusterName>-kubeconfig"
// Secret and returns its "value" key as a string. client-go already decodes
// Secret data from the wire format, so no further base64 handling is needed.
func FetchWorkloadKubeconfig(ctx context.Context, core corev1client.CoreV1Interface, namespace, clusterName string) (string, error) {
	name := clusterName + "-kubeconfig"
	secret, err := core.Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("capi: fetching secret %s/%s: %w", namespace, name, err)
	}
	value, ok := secret.Data["value"]
	if !ok || len(value) == 0 {
		return "", fmt.Errorf("fetchWorkloadKubeconfig: secret %q has no 'value' key", name)
	}
	return string(value), nil
}
