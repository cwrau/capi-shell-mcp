package kubectlargs

import (
	"strings"
	"testing"
)

func TestValidateAllowsReadOnlyInvocations(t *testing.T) {
	cases := [][]string{
		{"get", "pods"},
		{"get", "pods", "-n", "kube-system"},
		{"get", "pods", "--namespace=kube-system", "-o", "wide"},
		{"get", "pods", "-A", "-l", "app=nginx,tier!=db"},
		{"get", "nodes", "-o", "jsonpath={.items[*].metadata.name}"},
		{"get", "pods", "-o=custom-columns=NAME:.metadata.name"},
		{"get", "deploy", "-o", "go-template={{range .items}}{{.metadata.name}}{{end}}"},
		{"get", "pods", "--field-selector", "status.phase=Running", "--sort-by=.metadata.name", "--no-headers"},
		{"get", "pods", "--ignore-not-found=true"},
		{"describe", "pod", "foo", "-n", "default", "--show-events=false"},
		{"logs", "foo", "-c", "app", "--tail", "100", "--since=5m", "--timestamps", "-p"},
		{"logs", "foo", "--tail", "-1"},
		{"top", "pods", "-A", "--containers"},
		{"events", "-n", "default", "--for", "pod/foo", "--types=Warning"},
		{"api-resources", "--namespaced=false", "--api-group", "apps", "-o", "name"},
		{"api-versions"},
		{"explain", "pods.spec", "--recursive", "-o", "plaintext"},
		{"version", "--client", "-o", "json"},
		{"auth", "can-i", "list", "pods", "-n", "default"},
		{"auth", "whoami", "-o", "json"},
	}
	for _, args := range cases {
		if err := Validate(args); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", args, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"empty", nil, "must not be empty"},
		{"not a verb", []string{"sh"}, "not allowed"},
		{"leading flag hides the verb", []string{"--kubeconfig=/x", "get", "pods"}, "not allowed"},
		{"write verb", []string{"delete", "pod", "foo"}, "not allowed"},
		{"exec verb", []string{"exec", "foo", "--", "id"}, "not allowed"},
		{"config verb reads kubeconfigs", []string{"config", "view"}, "not allowed"},
		{"plugin dispatch", []string{"oidc-login", "get-token"}, "not allowed"},
		{"plugin subcommand", []string{"plugin", "list"}, "not allowed"},
		{"kubeconfig override", []string{"get", "pods", "--kubeconfig=/home/u/.config/kube/example"}, "--kubeconfig"},
		{"kubeconfig override separate value", []string{"get", "pods", "--kubeconfig", "/x"}, "--kubeconfig"},
		{"token", []string{"get", "pods", "--token=abc"}, "--token"},
		{"impersonation", []string{"get", "pods", "--as=system:admin"}, "--as"},
		{"server override", []string{"get", "pods", "--server=https://evil"}, "--server"},
		{"context override", []string{"get", "pods", "--context=other"}, "--context"},
		{"file input", []string{"get", "-f", "/etc/passwd"}, "-f"},
		{"filename long", []string{"get", "--filename=/etc/passwd"}, "--filename"},
		{"kustomize", []string{"get", "-k", "/x"}, "-k"},
		{"watch", []string{"get", "pods", "-w"}, "-w"},
		{"follow logs", []string{"logs", "foo", "-f"}, "-f"},
		{"raw", []string{"get", "--raw", "/api"}, "--raw"},
		{"verbosity", []string{"get", "pods", "-v=9"}, "-v"},
		{"profile output writes files", []string{"get", "pods", "--profile=cpu", "--profile-output=/tmp/x"}, "--profile"},
		{"go-template-file reads local files", []string{"get", "pods", "-o", "go-template-file=/etc/passwd"}, "go-template-file"},
		{"jsonpath-file", []string{"get", "pods", "-o=jsonpath-file=/etc/passwd"}, "jsonpath-file"},
		{"custom-columns-file", []string{"get", "pods", "--output=custom-columns-file=/etc/passwd"}, "custom-columns-file"},
		{"templatefile", []string{"get", "pods", "-o", "templatefile=/etc/passwd"}, "templatefile"},
		{"explain with printer format", []string{"explain", "pods", "-o", "json"}, "not allowed for explain"},
		{"attached short value", []string{"get", "pods", "-nkube-system"}, "-nkube-system"},
		{"short flag cluster", []string{"get", "pods", "-Ao", "wide"}, "-Ao"},
		{"end of options", []string{"get", "pods", "--", "-f", "/x"}, `"--"`},
		{"missing value", []string{"get", "pods", "-n"}, "requires a value"},
		{"bool with junk value", []string{"get", "pods", "-A=maybe"}, "boolean"},
		{"flag from another verb", []string{"get", "pods", "--tail", "5"}, "--tail"},
		{"auth without subcommand", []string{"auth"}, "subcommands"},
		{"auth reconcile writes RBAC", []string{"auth", "reconcile", "-f", "/x"}, "subcommands"},
		{"NUL byte", []string{"get", "pods\x00"}, "NUL"},
	}
	for _, tc := range cases {
		err := Validate(tc.args)
		if err == nil {
			t.Errorf("%s: Validate(%q) = nil, want an error", tc.name, tc.args)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Validate(%q) = %q, want it to mention %q", tc.name, tc.args, err, tc.want)
		}
	}
}

func TestValidateConsumesFlagValueLikePflagWouldNotTreatItAsAFlag(t *testing.T) {
	// pflag takes the token after a value flag as that flag's value even if
	// it starts with "-", so `-l --kubeconfig=/x` sets the selector, it does
	// not set a kubeconfig. Validate must agree, or the two would desync.
	if err := Validate([]string{"get", "pods", "-l", "--kubeconfig=/x"}); err != nil {
		t.Errorf("a flag-looking token consumed as a selector value must validate, got %v", err)
	}
}
