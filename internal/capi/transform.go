package capi

import (
	"fmt"
	"strings"

	"github.com/cwrau/capi-shell-mcp/internal/config"
	"github.com/itchyny/gojq"
	"gopkg.in/yaml.v3"
)

// ApplyKubeconfigTransform runs the configured jq expressions over
// kcYAML's .clusters/.contexts/.users arrays and returns the transformed
// kubeconfig as YAML.
func ApplyKubeconfigTransform(kcYAML string, transforms config.KubeconfigTransforms) (string, error) {
	var parts []string
	if transforms.Clusters != "" {
		parts = append(parts, fmt.Sprintf(".clusters |= map(%s)", transforms.Clusters))
	}
	if transforms.Contexts != "" {
		parts = append(parts, fmt.Sprintf(".contexts |= map(%s)", transforms.Contexts))
	}
	if transforms.Users != "" {
		parts = append(parts, fmt.Sprintf(".users |= map(%s)", transforms.Users))
	}

	query, err := gojq.Parse(strings.Join(parts, " | "))
	if err != nil {
		return "", err
	}
	code, err := gojq.Compile(query)
	if err != nil {
		return "", err
	}

	var input any
	if err := yaml.Unmarshal([]byte(kcYAML), &input); err != nil {
		return "", fmt.Errorf("capi: parsing kubeconfig for transform: %w", err)
	}

	iter := code.Run(input)
	v, ok := iter.Next()
	if !ok {
		return "", fmt.Errorf("jq: transform produced no output")
	}
	if errVal, ok := v.(error); ok {
		return "", errVal
	}

	out, err := yaml.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("capi: rendering transformed kubeconfig: %w", err)
	}
	return string(out), nil
}
