package capi

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestFetchWorkloadKubeconfigReturnsSecretValue(t *testing.T) {
	rawKubeconfig := "apiVersion: v1\nclusters: []"
	client := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-1-kubeconfig", Namespace: "ns-1"},
		Data:       map[string][]byte{"value": []byte(rawKubeconfig)},
	})

	got, err := FetchWorkloadKubeconfig(context.Background(), client.CoreV1(), "ns-1", "cluster-1")
	if err != nil {
		t.Fatalf("FetchWorkloadKubeconfig: %v", err)
	}
	if got != rawKubeconfig {
		t.Errorf("got %q, want %q", got, rawKubeconfig)
	}
}

func TestFetchWorkloadKubeconfigErrorsWhenValueKeyMissing(t *testing.T) {
	client := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-1-kubeconfig", Namespace: "ns-1"},
		Data:       map[string][]byte{},
	})

	_, err := FetchWorkloadKubeconfig(context.Background(), client.CoreV1(), "ns-1", "cluster-1")
	if err == nil || !strings.Contains(err.Error(), "value") {
		t.Fatalf("err = %v, want mention of missing 'value' key", err)
	}
}
