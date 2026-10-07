package capi

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	rbacv1client "k8s.io/client-go/kubernetes/typed/rbac/v1"
)

const (
	readOnlySAName    = "capi-shell-mcp-read-only"
	readOnlyNamespace = "kube-system"

	// readOnlyImpersonationUser is the username Kubernetes derives for the
	// read-only ServiceAccount.
	readOnlyImpersonationUser = "system:serviceaccount:" + readOnlyNamespace + ":" + readOnlySAName

	// readOnlyRoleName names both the ClusterRole we generate and the
	// ClusterRoleBinding that gives it to the ServiceAccount.
	readOnlyRoleName = readOnlySAName

	// viewBindingName is the ClusterRoleBinding to the builtin "view" role
	// an earlier version created. It is removed wherever it is still found,
	// so the ServiceAccount is bound to our generated role only.
	viewBindingName = readOnlySAName + "-view"
)

// readOnlySubresources are the only subresources the generated role reads.
// Everything else (exec, attach, portforward, proxy, token, ...) is a
// connection or an action that happens to be reachable with GET.
var readOnlySubresources = map[string]bool{"status": true, "scale": true, "log": true}

// APIResourceLister is the part of a discovery client the read-only role
// generation needs.
type APIResourceLister interface {
	ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error)
}

func isImmutableRoleRefError(err error) bool {
	return apierrors.IsInvalid(err) && strings.Contains(err.Error(), "roleRef") && strings.Contains(err.Error(), "immutable")
}

// deniedEntry is one entry of the deny list, already checked by the config
// loader: "secrets" is a core resource, "apps/deployments" a resource in a
// group, and "apps/v1/deployments" the same with a version, like the API
// path. The core group may also be written as an empty group ("/secrets") or
// with just a version ("v1/secrets").
//
// RBAC does not tell versions apart, so a version only identifies the group
// and the resource is denied in every version.
type deniedEntry struct{ group, resource string }

// versionLike matches API versions such as v1, v2beta1 and v1alpha3.
var versionLike = regexp.MustCompile(`^v[0-9]+((alpha|beta)[0-9]+)?$`)

func parseDenied(denied []string) []deniedEntry {
	out := make([]deniedEntry, 0, len(denied))
	for _, d := range denied {
		parts := strings.Split(strings.ToLower(strings.TrimSpace(d)), "/")
		e := deniedEntry{resource: parts[len(parts)-1]}
		switch {
		case len(parts) >= 3:
			e.group = parts[len(parts)-3]
		case len(parts) == 2 && !versionLike.MatchString(parts[0]):
			e.group = parts[0]
		}
		out = append(out, e)
	}
	return out
}

func anyDenies(entries []deniedEntry, group, resource string) bool {
	for _, e := range entries {
		if e.resource == resource && e.group == group {
			return true
		}
	}
	return false
}

// readOnlyRules generates the rules of the read-only ClusterRole.
//
// RBAC has no deny rules, and a resource wildcard would also reach the
// subresources that merely answer GET (exec, portforward, proxy, ...). So
// the role always lists every readable resource the cluster serves right
// now, minus the denied ones, even when nothing is denied (then Secrets
// are listed too); a resource that appears later is picked up the next
// time the role is generated.
func readOnlyRules(denied []string, lister APIResourceLister) ([]rbacv1.PolicyRule, error) {
	nonResource := rbacv1.PolicyRule{NonResourceURLs: []string{"*"}, Verbs: []string{"get"}}
	_, lists, err := lister.ServerGroupsAndResources()
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
		return nil, fmt.Errorf("capi: discovering API resources for the read-only role: %w", err)
	}
	deniedEntries := parseDenied(denied)
	byGroup := map[string]map[string]bool{}
	for _, list := range lists {
		gv, parseErr := schema.ParseGroupVersion(list.GroupVersion)
		if parseErr != nil {
			return nil, fmt.Errorf("capi: parsing group version %q from discovery: %w", list.GroupVersion, parseErr)
		}
		for _, r := range list.APIResources {
			resource, sub, isSub := strings.Cut(r.Name, "/")
			if isSub && !readOnlySubresources[sub] {
				continue
			}
			if anyDenies(deniedEntries, gv.Group, resource) {
				continue
			}
			if !hasVerb(r.Verbs, "get") && !hasVerb(r.Verbs, "list") {
				continue
			}
			if byGroup[gv.Group] == nil {
				byGroup[gv.Group] = map[string]bool{}
			}
			byGroup[gv.Group][r.Name] = true
		}
	}
	if len(byGroup) == 0 {
		return nil, errors.New("capi: discovery returned no readable API resources, refusing to generate an empty read-only role")
	}

	groups := make([]string, 0, len(byGroup))
	for g := range byGroup {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	rules := make([]rbacv1.PolicyRule, 0, len(groups)+1)
	for _, g := range groups {
		resources := make([]string, 0, len(byGroup[g]))
		for r := range byGroup[g] {
			resources = append(resources, r)
		}
		sort.Strings(resources)
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{g}, Resources: resources, Verbs: []string{"get", "list", "watch"}})
	}
	return append(rules, nonResource), nil
}

func hasVerb(verbs metav1.Verbs, verb string) bool {
	for _, v := range verbs {
		if v == verb {
			return true
		}
	}
	return false
}

// ensureReadOnlyClusterRole creates the generated ClusterRole or, when its
// rules differ from what they should be, replaces them. An unchanged role
// is left alone, so repeated calls do not write.
func ensureReadOnlyClusterRole(ctx context.Context, rbac rbacv1client.RbacV1Interface, rules []rbacv1.PolicyRule) error {
	desired := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: readOnlyRoleName}, Rules: rules}
	// Fan-out calls reach the same cluster at the same time, so creating and
	// updating can race with another call doing the same: start over when
	// the role appeared or changed in between.
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		var existing *rbacv1.ClusterRole
		existing, err = rbac.ClusterRoles().Get(ctx, readOnlyRoleName, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			_, err = rbac.ClusterRoles().Create(ctx, desired, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("capi: creating read-only ClusterRole: %w", err)
			}
			return nil
		case err != nil:
			return fmt.Errorf("capi: reading read-only ClusterRole: %w", err)
		}
		if apiequality.Semantic.DeepEqual(existing.Rules, rules) && existing.AggregationRule == nil {
			return nil
		}
		updated := existing.DeepCopy()
		updated.Rules = rules
		updated.AggregationRule = nil
		_, err = rbac.ClusterRoles().Update(ctx, updated, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("capi: replacing read-only ClusterRole: %w", err)
		}
		return nil
	}
	return fmt.Errorf("capi: read-only ClusterRole kept changing underneath us: %w", err)
}

// ensureClusterRoleBinding idempotently creates or replaces a
// ClusterRoleBinding, recovering from the immutable roleRef error by
// deleting and recreating it when the roleRef itself has changed.
func ensureClusterRoleBinding(ctx context.Context, rbac rbacv1client.RbacV1Interface, binding *rbacv1.ClusterRoleBinding) error {
	_, err := rbac.ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("capi: creating %s ClusterRoleBinding: %w", binding.Name, err)
	}

	if _, err := rbac.ClusterRoleBindings().Update(ctx, binding, metav1.UpdateOptions{}); err != nil {
		if !isImmutableRoleRefError(err) {
			return fmt.Errorf("capi: replacing %s ClusterRoleBinding: %w", binding.Name, err)
		}
		if err := rbac.ClusterRoleBindings().Delete(ctx, binding.Name, metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("capi: deleting %s ClusterRoleBinding for recreate: %w", binding.Name, err)
		}
		if _, err := rbac.ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("capi: recreating %s ClusterRoleBinding: %w", binding.Name, err)
		}
	}
	return nil
}

// EnsureReadOnlyRBAC idempotently ensures the ServiceAccount, its generated
// ClusterRole and the ClusterRoleBinding backing the read-only kubeconfig
// exist and are up to date, and that the binding to the builtin "view" role
// an earlier version created is gone.
//
// The role grants get, list and watch on every API resource the cluster
// serves except the denied ones (see deniedEntry). Which
// resources those are is the operator's decision: the daemon's
// readonly.denied_resources setting, which defaults to Secrets only and,
// when empty, denies nothing.
//
// If the cluster's API resources cannot be discovered, no role is written:
// the failure is returned instead of falling back to a wider role. When
// only some API groups fail discovery (an unavailable aggregated API), the
// role is generated from the groups that answered: the others are simply
// not readable until a later call finds them again, never readable by
// accident.
func EnsureReadOnlyRBAC(ctx context.Context, core corev1client.CoreV1Interface, rbac rbacv1client.RbacV1Interface, lister APIResourceLister, denied []string) error {
	rules, err := readOnlyRules(denied, lister)
	if err != nil {
		return err
	}

	_, err = core.ServiceAccounts(readOnlyNamespace).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: readOnlySAName, Namespace: readOnlyNamespace},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("capi: creating read-only ServiceAccount: %w", err)
	}

	if err := ensureReadOnlyClusterRole(ctx, rbac, rules); err != nil {
		return err
	}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: readOnlyRoleName},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: readOnlyRoleName},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: readOnlySAName, Namespace: readOnlyNamespace}},
	}
	if err := ensureClusterRoleBinding(ctx, rbac, binding); err != nil {
		return err
	}
	if err := rbac.ClusterRoleBindings().Delete(ctx, viewBindingName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("capi: deleting the read-only ServiceAccount's binding to the builtin view role: %w", err)
	}
	return nil
}

// ReadOnlyKubeconfig ensures the read-only RBAC exists and returns the
// admin kubeconfig reduced to its current user, set to impersonate the
// read-only ServiceAccount (`as`). The caller's own credential still
// authenticates the request, so the audit log attributes it to them with
// the ServiceAccount as impersonatedUser, while RBAC is evaluated against
// the ServiceAccount. The caller's user needs the `impersonate` verb on the
// ServiceAccount.
//
// It never reads from a cache: callers get a fresh RBAC-ensure on every
// call, by design.
func ReadOnlyKubeconfig(ctx context.Context, core corev1client.CoreV1Interface, rbac rbacv1client.RbacV1Interface, lister APIResourceLister, denied []string, adminKcYAML string) (string, error) {
	var kc map[string]any
	if err := yaml.Unmarshal([]byte(adminKcYAML), &kc); err != nil {
		return "", fmt.Errorf("capi: parsing admin kubeconfig: %w", err)
	}
	entry, err := currentUserEntry(kc)
	if err != nil {
		return "", err
	}

	if err := EnsureReadOnlyRBAC(ctx, core, rbac, lister, denied); err != nil {
		return "", err
	}

	user, _ := entry["user"].(map[string]any)
	if user == nil {
		user = map[string]any{}
		entry["user"] = user
	}
	user["as"] = readOnlyImpersonationUser
	delete(user, "as-groups")
	delete(user, "as-uid")
	delete(user, "as-user-extra")

	// Only the current user goes to the consumer; other users' credentials
	// in the kubeconfig are none of its business.
	kc["users"] = []any{entry}

	b, err := yaml.Marshal(kc)
	if err != nil {
		return "", fmt.Errorf("capi: rendering read-only kubeconfig: %w", err)
	}
	return string(b), nil
}

// currentUserEntry returns the users[] entry the current context refers to.
func currentUserEntry(kc map[string]any) (map[string]any, error) {
	current, _ := kc["current-context"].(string)
	contexts, _ := kc["contexts"].([]any)
	var userName string
	for _, c := range contexts {
		cm, _ := c.(map[string]any)
		if cm == nil || cm["name"] != current {
			continue
		}
		ctxBody, _ := cm["context"].(map[string]any)
		userName, _ = ctxBody["user"].(string)
	}
	if userName == "" {
		return nil, errors.New("readOnlyKubeconfig: current context of admin kubeconfig names no user")
	}
	users, _ := kc["users"].([]any)
	for _, u := range users {
		if um, ok := u.(map[string]any); ok && um["name"] == userName {
			return um, nil
		}
	}
	return nil, fmt.Errorf("readOnlyKubeconfig: user %q of admin kubeconfig's current context not found", userName)
}
