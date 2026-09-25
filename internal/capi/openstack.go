package capi

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

func fetchOpenStackCluster(ctx context.Context, dyn dynamic.Interface, namespace, clusterName string) (*unstructured.Unstructured, error) {
	list, err := dyn.Resource(openStackGVR).Namespace(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "cluster.x-k8s.io/cluster-name=" + clusterName,
	})
	if err != nil {
		return nil, fmt.Errorf("capi: listing OpenStackClusters: %w", err)
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	return &list.Items[0], nil
}

type cloudsYAML struct {
	Clouds map[string]struct {
		Auth               map[string]string `yaml:"auth"`
		RegionName         string            `yaml:"region_name"`
		Cacert             string            `yaml:"cacert"`
		AuthType           string            `yaml:"auth_type"`
		Interface          string            `yaml:"interface"`
		IdentityAPIVersion string            `yaml:"identity_api_version"`
	} `yaml:"clouds"`
}

// FetchOpenStackEnv reads the OpenStackCluster CR for clusterName and its
// referenced clouds.yaml Secret, returning OS_* environment variables for
// the named cloud.
func FetchOpenStackEnv(ctx context.Context, dyn dynamic.Interface, core corev1client.CoreV1Interface, namespace, clusterName string) (map[string]string, error) {
	osc, err := fetchOpenStackCluster(ctx, dyn, namespace, clusterName)
	if err != nil {
		return nil, err
	}
	if osc == nil {
		return nil, fmt.Errorf("No OpenStackCluster found for cluster '%s' in namespace '%s'", clusterName, namespace)
	}

	secretName, _, _ := unstructured.NestedString(osc.Object, "spec", "identityRef", "name")
	if secretName == "" {
		secretName = clusterName + "-cloud-config"
	}
	cloudName, _, _ := unstructured.NestedString(osc.Object, "spec", "cloudName")
	if cloudName == "" {
		cloudName = "openstack"
	}

	secret, err := core.Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("capi: fetching secret %s/%s: %w", namespace, secretName, err)
	}
	raw, ok := secret.Data["clouds.yaml"]
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("Secret '%s' has no 'clouds.yaml' key", secretName)
	}

	var clouds cloudsYAML
	if err := yaml.Unmarshal(raw, &clouds); err != nil {
		return nil, fmt.Errorf("capi: parsing clouds.yaml: %w", err)
	}
	cloud, ok := clouds.Clouds[cloudName]
	if !ok {
		return nil, fmt.Errorf("OpenStack cloud '%s' not found in clouds.yaml", cloudName)
	}

	env := make(map[string]string)
	set := func(k, v string) {
		if v != "" {
			env[k] = v
		}
	}
	set("OS_AUTH_URL", cloud.Auth["auth_url"])
	set("OS_USERNAME", cloud.Auth["username"])
	set("OS_PASSWORD", cloud.Auth["password"])
	set("OS_PROJECT_NAME", cloud.Auth["project_name"])
	set("OS_USER_DOMAIN_NAME", cloud.Auth["user_domain_name"])
	set("OS_PROJECT_DOMAIN_NAME", cloud.Auth["project_domain_name"])
	set("OS_REGION_NAME", cloud.RegionName)
	set("OS_CACERT", cloud.Cacert)
	set("OS_APPLICATION_CREDENTIAL_ID", cloud.Auth["application_credential_id"])
	set("OS_APPLICATION_CREDENTIAL_SECRET", cloud.Auth["application_credential_secret"])
	authType := cloud.AuthType
	if authType == "" {
		authType = cloud.Auth["auth_type"]
	}
	set("OS_AUTH_TYPE", authType)
	set("OS_INTERFACE", cloud.Interface)
	set("OS_IDENTITY_API_VERSION", cloud.IdentityAPIVersion)

	return env, nil
}

// FetchAPIServerInfo returns the workload cluster's API server host/port
// and ok=true only when its OpenStackCluster restricts allowedCIDRs
// (implying the MCP host likely needs an sshuttle tunnel to reach it).
func FetchAPIServerInfo(ctx context.Context, dyn dynamic.Interface, namespace, clusterName string) (host, port string, ok bool, err error) {
	osc, err := fetchOpenStackCluster(ctx, dyn, namespace, clusterName)
	if err != nil {
		return "", "", false, err
	}
	if osc == nil {
		return "", "", false, nil
	}
	cidrs, _, _ := unstructured.NestedStringSlice(osc.Object, "spec", "apiServerLoadBalancer", "allowedCIDRs")
	if len(cidrs) == 0 {
		return "", "", false, nil
	}
	host, _, _ = unstructured.NestedString(osc.Object, "spec", "controlPlaneEndpoint", "host")
	port = nestedStringOrNumber(osc.Object, "spec", "controlPlaneEndpoint", "port")
	if host == "" || port == "" {
		return "", "", false, nil
	}
	return host, port, true, nil
}

// nestedStringOrNumber reads a field that may be encoded as either a JSON
// string or number (API server ports typically decode as int64/float64).
func nestedStringOrNumber(obj map[string]any, fields ...string) string {
	v, found, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	if !found {
		return ""
	}
	switch n := v.(type) {
	case string:
		return n
	case int64:
		return fmt.Sprintf("%d", n)
	case float64:
		return fmt.Sprintf("%d", int64(n))
	default:
		return ""
	}
}
