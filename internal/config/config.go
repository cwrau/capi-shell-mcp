// Package config loads capi-shell-mcp's YAML configuration file.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type KubeconfigTransforms struct {
	Clusters string
	Contexts string
	Users    string
}

// KubeconfigSource is one named kubeconfig entry — a kubeconfig file path
// plus an optional list of contexts to restrict discovery to. Not
// necessarily a CAPI "management cluster": it's just a kubeconfig to read
// Cluster CRs and Secrets from.
type KubeconfigSource struct {
	Name         string
	Path         string
	Contexts     []string
	SshuttleHost string
}

type CacheConfig struct {
	ClusterListTTLSeconds int
	KubeconfigTTLSeconds  int
}

// ReadOnlyConfig configures the ClusterRole the read-only ServiceAccount is
// bound to on every workload cluster.
type ReadOnlyConfig struct {
	// DeniedResources are the resources the role does not grant read access
	// to: "secrets" for a core resource, "group/resource" for any other
	// ("apps/deployments"), see normalizeDeniedResource. An empty list denies
	// nothing, so Secrets are readable too. LoadConfig fills in
	// DefaultDeniedResources when the setting is absent.
	DeniedResources []string
}

type AppConfig struct {
	Kubeconfigs  map[string]KubeconfigSource
	Transforms   *KubeconfigTransforms
	CustomFields map[string]string
	SshuttleHost string
	Cache        CacheConfig
	ReadOnly     ReadOnlyConfig
}

const (
	defaultClusterListTTLSeconds = 300
	defaultKubeconfigTTLSeconds  = 3600
)

// DefaultDeniedResources is what the read-only role keeps back when the
// configuration does not say otherwise.
func DefaultDeniedResources() []string { return []string{"secrets"} }

var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

func expandEnv(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		groups := envVarPattern.FindStringSubmatch(match)
		name := groups[1]
		if name == "" {
			name = groups[2]
		}
		return os.Getenv(name)
	})
}

// configPath returns the config file path: CAPI_SHELL_MCP_CONFIG if set,
// else ~/.config/capi-shell/config.yaml.
func configPath() (string, error) {
	if p := os.Getenv("CAPI_SHELL_MCP_CONFIG"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolving home directory: %w", err)
	}
	return filepath.Join(home, ".config", "capi-shell", "config.yaml"), nil
}

// Load resolves the config path (env var or default) and loads it.
func Load() (*AppConfig, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	return LoadConfig(path)
}

// LoadConfig loads and parses the YAML config file at path.
func LoadConfig(path string) (*AppConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: reading %s: %w", path, err)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config: parsing %s: %w", path, err)
	}

	rawKubeconfigs, ok := raw["kubeconfigs"].(map[string]any)
	if !ok || len(rawKubeconfigs) == 0 {
		return nil, fmt.Errorf("config: kubeconfigs must be a non-empty map of name to config")
	}

	kubeconfigs := make(map[string]KubeconfigSource, len(rawKubeconfigs))
	for name, v := range rawKubeconfigs {
		entry, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("config: kubeconfigs.%s must be an object", name)
		}
		path, ok := entry["path"].(string)
		if !ok {
			return nil, fmt.Errorf("config: kubeconfigs.%s.path must be a string", name)
		}
		kubeconfigs[name] = KubeconfigSource{
			Name:         name,
			Path:         expandEnv(path),
			Contexts:     parseContexts(entry["contexts"]),
			SshuttleHost: readSshuttleHost(entry),
		}
	}

	cache := CacheConfig{
		ClusterListTTLSeconds: defaultClusterListTTLSeconds,
		KubeconfigTTLSeconds:  defaultKubeconfigTTLSeconds,
	}
	if rawCache, ok := raw["cache"].(map[string]any); ok {
		if v, ok := toInt(rawCache["cluster_list_ttl"]); ok {
			cache.ClusterListTTLSeconds = v
		}
		if v, ok := toInt(rawCache["kubeconfig_ttl"]); ok {
			cache.KubeconfigTTLSeconds = v
		}
	}

	readOnly, err := parseReadOnly(raw["readonly"])
	if err != nil {
		return nil, err
	}

	return &AppConfig{
		Kubeconfigs:  kubeconfigs,
		Transforms:   parseTransforms(raw["transforms"]),
		CustomFields: parseCustomFields(raw["custom_fields"]),
		SshuttleHost: readSshuttleHost(raw),
		Cache:        cache,
		ReadOnly:     readOnly,
	}, nil
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

func parseTransforms(raw any) *KubeconfigTransforms {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	result := KubeconfigTransforms{}
	found := false
	if s, ok := obj["clusters"].(string); ok {
		result.Clusters = s
		found = true
	}
	if s, ok := obj["contexts"].(string); ok {
		result.Contexts = s
		found = true
	}
	if s, ok := obj["users"].(string); ok {
		result.Users = s
		found = true
	}
	if !found {
		return nil
	}
	return &result
}

// parseContexts reads a YAML list of context names, e.g.:
//
//	contexts:
//	  - dev-admin
//	  - dev-viewer
//
// An absent or empty list means "every context in this kubeconfig".
func parseContexts(raw any) []string {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func parseCustomFields(raw any) map[string]string {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	result := make(map[string]string)
	for k, v := range obj {
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// readSshuttleHost reads plugins.api-endpoint-proxy.sshuttle.host from raw,
// returning "" if any segment of that path is absent or the wrong type.
func readSshuttleHost(raw map[string]any) string {
	plugins, ok := raw["plugins"].(map[string]any)
	if !ok {
		return ""
	}
	proxyPlugin, ok := plugins["api-endpoint-proxy"].(map[string]any)
	if !ok {
		return ""
	}
	sshuttle, ok := proxyPlugin["sshuttle"].(map[string]any)
	if !ok {
		return ""
	}
	host, ok := sshuttle["host"].(string)
	if !ok {
		return ""
	}
	return expandEnv(host)
}

// parseReadOnly reads the optional "readonly" section:
//
//	readonly:
//	  denied_resources: [secrets, apps/deployments]
//
// A missing section, a missing or null denied_resources, or an empty
// section all mean the default (Secrets only). An explicit empty list
// ([]) means nothing is denied.
func parseReadOnly(raw any) (ReadOnlyConfig, error) {
	if raw == nil {
		return ReadOnlyConfig{DeniedResources: DefaultDeniedResources()}, nil
	}
	section, ok := raw.(map[string]any)
	if !ok {
		return ReadOnlyConfig{}, fmt.Errorf("config: readonly must be an object")
	}
	rawDenied, present := section["denied_resources"]
	if !present || rawDenied == nil {
		return ReadOnlyConfig{DeniedResources: DefaultDeniedResources()}, nil
	}
	list, ok := rawDenied.([]any)
	if !ok {
		return ReadOnlyConfig{}, fmt.Errorf("config: readonly.denied_resources must be a list of resource names")
	}
	denied := make([]string, 0, len(list))
	for i, v := range list {
		s, ok := v.(string)
		if !ok {
			return ReadOnlyConfig{}, fmt.Errorf("config: readonly.denied_resources[%d] must be a string", i)
		}
		s, err := normalizeDeniedResource(s)
		if err != nil {
			return ReadOnlyConfig{}, fmt.Errorf("config: readonly.denied_resources[%d]: %w", i, err)
		}
		denied = append(denied, s)
	}
	return ReadOnlyConfig{DeniedResources: denied}, nil
}

var (
	versionPattern = regexp.MustCompile(`^v[0-9]+((alpha|beta)[0-9]+)?$`)
	// dotlessGroups are the API groups without a dot in their name. Every
	// other group, CRD groups included, has one, so a dotless first part
	// that is not in this list is a typo or a subresource.
	dotlessGroups = map[string]bool{"": true, "apps": true, "batch": true, "policy": true, "autoscaling": true, "extensions": true}
)

// normalizeDeniedResource checks one denied_resources entry and returns it
// trimmed and lowercased. The notation is
//
//	resource                 a core resource:        secrets
//	group/resource           anything else:          apps/deployments
//	group/version/resource   like the API path:      apps/v1/deployments
//
// The core group may be written as an empty group (/secrets, /v1/secrets)
// and a version alone also names it (v1/secrets). A version only identifies
// the group: RBAC does not tell versions apart, so the resource is denied in
// all of them. Subresources follow their parent and cannot be listed.
//
// Entries that could not match anything are rejected instead of accepted
// as a rule that silently denies nothing.
func normalizeDeniedResource(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	parts := strings.Split(s, "/")
	resource := parts[len(parts)-1]
	switch {
	case s == "" || resource == "":
		return "", fmt.Errorf("%q must name a resource, like \"secrets\" or \"apps/deployments\"", raw)
	case len(parts) > 3:
		return "", fmt.Errorf("%q has too many parts: write group/resource or group/version/resource; subresources follow their parent and cannot be listed", raw)
	case len(parts) == 1 && strings.Contains(resource, "."):
		name, group, _ := strings.Cut(resource, ".")
		return "", fmt.Errorf("%q looks like a resource with its group: write the group first, like %q", raw, group+"/"+name)
	case len(parts) == 3 && !versionPattern.MatchString(parts[1]):
		return "", fmt.Errorf("%q: the middle part must be an API version like v1, got %q", raw, parts[1])
	case len(parts) == 2 && !versionPattern.MatchString(parts[0]) && !strings.Contains(parts[0], ".") && !dotlessGroups[parts[0]]:
		return "", fmt.Errorf("%q: %q is no API group (is it a subresource? those follow their parent and cannot be listed)", raw, parts[0])
	case len(parts) == 3 && !strings.Contains(parts[0], ".") && !dotlessGroups[parts[0]]:
		return "", fmt.Errorf("%q: %q is no API group", raw, parts[0])
	}
	return s, nil
}
