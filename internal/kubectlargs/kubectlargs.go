// Package kubectlargs validates the arguments of a read-only kubectl
// invocation against a strict allowlist of verbs and, per verb, flags.
//
// An allowlist (rather than a denylist) is deliberate: kubectl has flags
// and behaviors that turn it into something much broader than "read a
// cluster" — --kubeconfig/--token/--as/--server repoint or re-credential
// it, -f/-k and the *-file output formats read arbitrary local files, and
// an unknown subcommand dispatches to any kubectl-* binary on PATH. Anything
// not listed here is rejected rather than reasoned about.
package kubectlargs

import (
	"fmt"
	"slices"
	"strings"
)

type spec struct {
	short string // e.g. "-n", or "" if none
	long  string // e.g. "--namespace"
	value bool   // takes a value (separate token or =value) rather than being a bool
	check func(value string) error
}

var (
	fNamespace    = spec{short: "-n", long: "--namespace", value: true}
	fAllNamespace = spec{short: "-A", long: "--all-namespaces"}
	fSelector     = spec{short: "-l", long: "--selector", value: true}
	fFieldSel     = spec{long: "--field-selector", value: true}
	fTemplate     = spec{long: "--template", value: true}
	fAllowMissing = spec{long: "--allow-missing-template-keys"}
	fShowLabels   = spec{long: "--show-labels"}
	fLabelColumns = spec{short: "-L", long: "--label-columns", value: true}
	fSortBy       = spec{long: "--sort-by", value: true}
	fNoHeaders    = spec{long: "--no-headers"}
	fIgnoreNF     = spec{long: "--ignore-not-found"}
	fShowKind     = spec{long: "--show-kind"}
	fChunkSize    = spec{long: "--chunk-size", value: true}
	fSubresource  = spec{long: "--subresource", value: true}
	fReqTimeout   = spec{long: "--request-timeout", value: true}
	fShowEvents   = spec{long: "--show-events"}
	fContainer    = spec{short: "-c", long: "--container", value: true}
	fTail         = spec{long: "--tail", value: true}
	fSince        = spec{long: "--since", value: true}
	fSinceTime    = spec{long: "--since-time", value: true}
	fTimestamps   = spec{long: "--timestamps"}
	fPrevious     = spec{short: "-p", long: "--previous"}
	fLimitBytes   = spec{long: "--limit-bytes", value: true}
	fPrefix       = spec{long: "--prefix"}
	fAllContainer = spec{long: "--all-containers"}
	fMaxLogReqs   = spec{long: "--max-log-requests", value: true}
	fContainers   = spec{long: "--containers"}
	fFor          = spec{long: "--for", value: true}
	fTypes        = spec{long: "--types", value: true}
	fAPIGroup     = spec{long: "--api-group", value: true}
	fNamespaced   = spec{long: "--namespaced"}
	fVerbs        = spec{long: "--verbs", value: true}
	fCached       = spec{long: "--cached"}
	fRecursive    = spec{long: "--recursive"}
	fAPIVersion   = spec{long: "--api-version", value: true}
	fClient       = spec{long: "--client"}
	fList         = spec{long: "--list"}
	fQuiet        = spec{short: "-q", long: "--quiet"}

	// Output formats differ by verb: most use kubectl's printer formats,
	// explain uses its own. Neither may include the *-file variants
	// (go-template-file, jsonpath-file, custom-columns-file, templatefile),
	// which read an arbitrary local file and print it.
	fOutput        = spec{short: "-o", long: "--output", value: true, check: checkPrinterFormat}
	fOutputExplain = spec{short: "-o", long: "--output", value: true, check: checkExplainFormat}
)

var verbs = map[string][]spec{
	"get": {
		fNamespace, fAllNamespace, fSelector, fFieldSel, fOutput, fTemplate, fAllowMissing,
		fShowLabels, fLabelColumns, fSortBy, fNoHeaders, fIgnoreNF, fShowKind, fChunkSize,
		fSubresource, fReqTimeout,
	},
	"describe": {fNamespace, fAllNamespace, fSelector, fShowEvents, fChunkSize, fReqTimeout},
	"logs": {
		fNamespace, fContainer, fTail, fSince, fSinceTime, fTimestamps, fPrevious, fLimitBytes,
		fPrefix, fAllContainer, fMaxLogReqs, fSelector, fReqTimeout,
	},
	"top":           {fNamespace, fAllNamespace, fSelector, fFieldSel, fContainers, fNoHeaders, fSortBy, fReqTimeout},
	"events":        {fNamespace, fAllNamespace, fFor, fTypes, fNoHeaders, fOutput, fReqTimeout},
	"api-resources": {fAPIGroup, fNamespaced, fVerbs, fCached, fOutput, fNoHeaders, fSortBy, fReqTimeout},
	"api-versions":  {fReqTimeout},
	"explain":       {fRecursive, fAPIVersion, fOutputExplain, fReqTimeout},
	"version":       {fClient, fOutput, fReqTimeout},
	// auth is further restricted to its read-only subcommands below.
	"auth": {fNamespace, fAllNamespace, fList, fSubresource, fNoHeaders, fQuiet, fOutput, fReqTimeout},
}

// authSubcommands are the only `kubectl auth` subcommands allowed;
// `auth reconcile` writes RBAC objects.
var authSubcommands = []string{"can-i", "whoami"}

var printerFormats = []string{
	"", "json", "yaml", "name", "wide", "jsonpath", "jsonpath-as-json", "go-template", "template", "custom-columns",
}

var explainFormats = []string{"plaintext", "plaintext-openapiv2"}

func formatName(value string) string {
	name, _, _ := strings.Cut(value, "=")
	return name
}

func checkPrinterFormat(value string) error {
	if !slices.Contains(printerFormats, formatName(value)) {
		return fmt.Errorf("output format %q is not allowed (file-reading formats like go-template-file are rejected); allowed: %s",
			formatName(value), strings.Join(printerFormats[1:], ", "))
	}
	return nil
}

func checkExplainFormat(value string) error {
	if !slices.Contains(explainFormats, value) {
		return fmt.Errorf("output format %q is not allowed for explain; allowed: %s", value, strings.Join(explainFormats, ", "))
	}
	return nil
}

func sortedVerbs() []string {
	out := make([]string, 0, len(verbs))
	for v := range verbs {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

func flagNames(flags []spec) string {
	names := make([]string, 0, len(flags))
	for _, f := range flags {
		if f.short != "" {
			names = append(names, f.short+"/"+f.long)
		} else {
			names = append(names, f.long)
		}
	}
	slices.Sort(names)
	return strings.Join(names, " ")
}

// Validate reports whether args (everything after "kubectl") is a
// permitted read-only invocation: its first token must be an allowed verb
// (no leading flags, so the verb can't be obscured), and every later
// token starting with "-" must be a flag allowed for that verb. Tokens
// not starting with "-" are positionals (resource types, names, ...).
func Validate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("args must not be empty; the first element is the kubectl verb, one of: %s", strings.Join(sortedVerbs(), ", "))
	}
	for _, a := range args {
		if strings.ContainsRune(a, 0) {
			return fmt.Errorf("args must not contain NUL bytes")
		}
	}

	verb := args[0]
	flags, ok := verbs[verb]
	if !ok {
		return fmt.Errorf("kubectl verb %q is not allowed (the first element must be the verb, with no leading flags); allowed verbs: %s",
			verb, strings.Join(sortedVerbs(), ", "))
	}

	rest := args[1:]
	if verb == "auth" {
		if len(rest) == 0 || !slices.Contains(authSubcommands, rest[0]) {
			return fmt.Errorf("kubectl auth requires one of the subcommands: %s", strings.Join(authSubcommands, ", "))
		}
		rest = rest[1:]
	}

	index := make(map[string]spec, len(flags)*2)
	for _, f := range flags {
		index[f.long] = f
		if f.short != "" {
			index[f.short] = f
		}
	}

	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		if tok == "--" {
			return fmt.Errorf(`"--" is not allowed`)
		}
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			continue // positional
		}

		name, value, hasValue := strings.Cut(tok, "=")
		f, ok := index[name]
		if !ok {
			return fmt.Errorf("flag %q is not allowed for `kubectl %s`; allowed flags: %s", name, verb, flagNames(flags))
		}

		if f.value {
			if !hasValue {
				i++
				if i >= len(rest) {
					return fmt.Errorf("flag %q requires a value", name)
				}
				value = rest[i]
			}
		} else if hasValue && value != "true" && value != "false" {
			return fmt.Errorf("flag %q is a boolean; got value %q", name, value)
		}

		if f.check != nil {
			if err := f.check(value); err != nil {
				return fmt.Errorf("flag %q: %w", name, err)
			}
		}
	}
	return nil
}
