// Package config loads capi-shell-mcp's YAML configuration file.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

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

type AppConfig struct {
	Kubeconfigs  map[string]KubeconfigSource
	Transforms   *KubeconfigTransforms
	CustomFields map[string]string
	SshuttleHost string
	Cache        CacheConfig
}

const (
	defaultClusterListTTLSeconds = 300
	defaultKubeconfigTTLSeconds  = 3600
)

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

	return &AppConfig{
		Kubeconfigs:  kubeconfigs,
		Transforms:   parseTransforms(raw["transforms"]),
		CustomFields: parseCustomFields(raw["custom_fields"]),
		SshuttleHost: readSshuttleHost(raw),
		Cache:        cache,
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
