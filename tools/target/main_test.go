package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stack is the shape a real listing has, taken off this repository's own dev
// stacks rather than invented: the ambiguity below is real — the vendored cert
// approver is a ConfigFile whose children include a Namespace and a
// ClusterRoleBinding of the same name — and the two groups are the URNs the
// merged-layer experiment produces.
var stack = []Resource{
	{
		URN:  "urn:pulumi:dev::cluster-services::pulumi:pulumi:Stack::cluster-services-dev",
		Type: "pulumi:pulumi:Stack", Name: "cluster-services-dev",
	},
	{
		URN:  "urn:pulumi:dev::cluster-services::pulumi:providers:kubernetes::k8s",
		Type: "pulumi:providers:kubernetes", Name: "k8s",
	},
	{
		URN:  "urn:pulumi:dev::cluster-services::kubernetes:helm.sh/v3:Release::cert-manager",
		Type: "kubernetes:helm.sh/v3:Release", Name: "cert-manager",
	},
	{
		URN:  "urn:pulumi:dev::cluster-services::kubernetes:yaml:ConfigFile::kubelet-serving-cert-approver",
		Type: "kubernetes:yaml:ConfigFile", Name: "kubelet-serving-cert-approver",
	},
	{
		URN: "urn:pulumi:dev::cluster-services::kubernetes:yaml:ConfigFile$kubernetes:core/v1:" +
			"Namespace::kubelet-serving-cert-approver",
		Type: "kubernetes:core/v1:Namespace", Name: "kubelet-serving-cert-approver",
	},
	{
		URN:  "urn:pulumi:dev::cluster-services::hetzner-iac:platform:Ingress::ingress",
		Type: "hetzner-iac:platform:Ingress", Name: "ingress",
	},
	{
		URN: "urn:pulumi:dev::cluster-services::hetzner-iac:platform:Ingress$kubernetes:helm.sh/v3:" +
			"Release::traefik",
		Type: "kubernetes:helm.sh/v3:Release", Name: "traefik",
	},
	{
		URN: "urn:pulumi:dev::cluster-services::hetzner-iac:platform:Ingress$hcloud:index/" +
			"loadBalancer:LoadBalancer::ingress-lb",
		Type: "hcloud:index/loadBalancer:LoadBalancer", Name: "ingress-lb",
	},
}

// TestMatch_ResolvesAName is the ordinary case, and the only one that ends in
// URNs.
func TestMatch_ResolvesAName(t *testing.T) {
	t.Parallel()

	urns, err := Match(stack, "cert-manager")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"urn:pulumi:dev::cluster-services::kubernetes:helm.sh/v3:Release::cert-manager",
	}, urns)
}

// TestMatch_RefusesANameTheStackDoesNotHave is the whole reason this tool
// exists.
//
// Pulumi's own answer to a --target that matches nothing is silent success:
// measured on the dev stack, `preview --target '**::Release::does-not-exist'`
// reported "24 unchanged" and exited zero. So a mistyped component would be an
// apply that claims to have worked.
func TestMatch_RefusesANameTheStackDoesNotHave(t *testing.T) {
	t.Parallel()

	_, err := Match(stack, "cert-manger")
	require.Error(t, err)

	message := err.Error()

	assert.Contains(t, message, "matches nothing")
	assert.Contains(t, message, "reports success and does nothing",
		"the error must say why this is refused rather than passed through")

	// The list is the remedy: somebody who mistyped needs the spelling.
	assert.Contains(t, message, "Release:cert-manager")
	assert.Contains(t, message, "Release:traefik")
}

// TestMatch_RefusesAnAmbiguousName rather than picking one.
//
// Real ambiguity, not contrived: the vendored approver is a ConfigFile and its
// children include a Namespace of the same name. Targeting "the" approver
// would silently take one of them.
func TestMatch_RefusesAnAmbiguousName(t *testing.T) {
	t.Parallel()

	_, err := Match(stack, "kubelet-serving-cert-approver")
	require.Error(t, err)

	message := err.Error()

	assert.Contains(t, message, "names 2 resources")

	// Every qualified form, so the tool does not advise one arbitrarily.
	assert.Contains(t, message, "ConfigFile:kubelet-serving-cert-approver")
	assert.Contains(t, message, "Namespace:kubelet-serving-cert-approver")
}

// TestMatch_AQualifiedNameResolvesTheAmbiguity closes the loop the previous
// test opens: the error tells the operator what to type, and typing it works.
func TestMatch_AQualifiedNameResolvesTheAmbiguity(t *testing.T) {
	t.Parallel()

	urns, err := Match(stack, "ConfigFile:kubelet-serving-cert-approver")
	require.NoError(t, err)
	require.Len(t, urns, 1)

	assert.Contains(t, urns[0], "kubernetes:yaml:ConfigFile::kubelet-serving-cert-approver")
	assert.NotContains(t, urns[0], "Namespace")
}

// TestMatch_AQualifiedNameWithTheWrongTypeIsStillARefusal keeps the qualified
// form honest. `Release:kubelet-serving-cert-approver` names nothing, and
// nothing is what it must say.
func TestMatch_AQualifiedNameWithTheWrongTypeIsStillARefusal(t *testing.T) {
	t.Parallel()

	_, err := Match(stack, "Release:kubelet-serving-cert-approver")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matches nothing")
}

// TestMatch_AGroupTakesItsNodeAndItsChildren is what makes a former layer
// addressable as one thing.
//
// Both halves. The children are what an apply is about; the node is what a
// destroy also has to remove, or the group's own entry is left in the state
// with nothing under it.
func TestMatch_AGroupTakesItsNodeAndItsChildren(t *testing.T) {
	t.Parallel()

	urns, err := Match(stack, "group:Ingress")
	require.NoError(t, err)
	require.Len(t, urns, 3)

	joined := strings.Join(urns, "\n")

	assert.Contains(t, joined, "hetzner-iac:platform:Ingress::ingress", "the group's own node")
	assert.Contains(t, joined, "Ingress$kubernetes:helm.sh/v3:Release::traefik")
	assert.Contains(t, joined, "Ingress$hcloud:index/loadBalancer:LoadBalancer::ingress-lb")

	// And nothing from the other half of the layer.
	assert.NotContains(t, joined, "cert-manager")
}

// TestMatch_AGroupThatIsNotThereIsARefusal, which is the state of every layer
// on main: groups are the merged-layer experiment, so this tool has to be
// useful and honest before any of them exists.
func TestMatch_AGroupThatIsNotThereIsARefusal(t *testing.T) {
	t.Parallel()

	_, err := Match(stack, "group:ClusterServices")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matches nothing")
}

// TestMatch_DoesNotMatchAGroupsChildrenByAccident guards the substring test
// matchGroup uses. `Ingress` appears inside other type tokens — a Kubernetes
// Ingress, for one — and only the `:Type$` form means "a child of".
func TestMatch_DoesNotMatchAGroupsChildrenByAccident(t *testing.T) {
	t.Parallel()

	withKubernetesIngress := append(slicesClone(stack), Resource{
		URN:  "urn:pulumi:dev::cluster-services::kubernetes:networking.k8s.io/v1:Ingress::argocd",
		Type: "kubernetes:networking.k8s.io/v1:Ingress", Name: "argocd",
	})

	urns, err := Match(withKubernetesIngress, "group:Ingress")
	require.NoError(t, err)

	// Three, not four. The Kubernetes Ingress has the same type LEAF, and
	// matching on the leaf alone took it — `group:Ingress` meant every Ingress
	// object in the cluster. A group's type must begin with this repository's
	// own package, and a child is matched by its parent's FULL type token.
	require.Len(t, urns, 3)
	assert.NotContains(t, strings.Join(urns, "\n"), "argocd",
		"a provider's resource with the same type leaf was taken as part of the group")
}

// TestMatch_AGroupIsNotAProvidersResourceOfTheSameName pins the package check,
// and the ORDER is the whole test.
//
// Without it, the group node is whichever resource with that type leaf the
// state lists first. Put a Kubernetes Ingress ahead of the group and the
// selector resolves to that one object instead of the former layer — a
// `--target` that silently means something else, which is worse than one that
// matches nothing.
func TestMatch_AGroupIsNotAProvidersResourceOfTheSameName(t *testing.T) {
	t.Parallel()

	providerFirst := append([]Resource{{
		URN:  "urn:pulumi:dev::cluster-services::kubernetes:networking.k8s.io/v1:Ingress::argocd",
		Type: "kubernetes:networking.k8s.io/v1:Ingress", Name: "argocd",
	}}, stack...)

	urns, err := Match(providerFirst, "group:Ingress")
	require.NoError(t, err)

	require.Len(t, urns, 3, "the group node was resolved to a provider's resource")
	assert.Contains(t, urns[0], GroupPackage+":platform:Ingress::ingress",
		"the first URN must be the group's own node")
	assert.NotContains(t, strings.Join(urns, "\n"), "argocd")
}

func TestTypeLeaf(t *testing.T) {
	t.Parallel()

	for token, want := range map[string]string{
		"kubernetes:helm.sh/v3:Release":          "Release",
		"hetzner-iac:platform:Ingress":           "Ingress",
		"hcloud:index/loadBalancer:LoadBalancer": "LoadBalancer",
		"pulumi:pulumi:Stack":                    "Stack",
		"bare":                                   "bare",
	} {
		assert.Equal(t, want, TypeLeaf(token), token)
	}
}

// TestResourcesIn_ReadsTheShapeTheCLIProduces holds the parser to the real
// output rather than to a struct somebody hoped for.
func TestResourcesIn_ReadsTheShapeTheCLIProduces(t *testing.T) {
	t.Parallel()

	// Trimmed from `pulumi stack --show-urns --output json`: the listing
	// carries much more, and this asserts the three fields that are read.
	const raw = `{
	  "organization": "oleg-tkachuk",
	  "project": "cluster-services",
	  "stack": "dev",
	  "resources": [
	    {"urn":"urn:pulumi:dev::cluster-services::kubernetes:helm.sh/v3:Release::cert-manager",
	     "type":"kubernetes:helm.sh/v3:Release","name":"cert-manager"}
	  ],
	  "outputs": {}
	}`

	resources, err := resourcesIn([]byte(raw))
	require.NoError(t, err)
	require.Len(t, resources, 1)

	assert.Equal(t, "cert-manager", resources[0].Name)
	assert.Equal(t, "kubernetes:helm.sh/v3:Release", resources[0].Type)
}

// TestResourcesIn_RefusesAnEmptyStack, because every selector would then be
// "matches nothing" and the operator would go looking for a typo in a name
// that is simply not deployed yet.
func TestResourcesIn_RefusesAnEmptyStack(t *testing.T) {
	t.Parallel()

	_, err := resourcesIn([]byte(`{"resources": []}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apply it before targeting part of it")
}

func TestResourcesIn_RefusesOutputItCannotRead(t *testing.T) {
	t.Parallel()

	_, err := resourcesIn([]byte("not json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse the stack listing")
}

// TestRun_RefusesTheWrongNumberOfArguments, and prints the selector forms —
// which is the only documentation an operator has at the moment they need it.
func TestRun_RefusesTheWrongNumberOfArguments(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{}, {"dir"}, {"dir", "stack"}, {"dir", "stack", "sel", "extra"}} {
		err := run(t.Context(), args, &strings.Builder{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "usage: target")
		assert.Contains(t, err.Error(), GroupPrefix+"Type")
	}
}

// TestRun_RefusesAnEmptySelector separately from the usage error: a task that
// passes `target=` with nothing in it is a bug in the task, and it must not
// read as an operator's typo.
func TestRun_RefusesAnEmptySelector(t *testing.T) {
	t.Parallel()

	err := run(t.Context(), []string{"dir", "dev", "  "}, &strings.Builder{})

	require.ErrorIs(t, err, ErrNoSelector)
}

// slicesClone keeps the shared fixture shared: append on a package-level slice
// can write into its backing array and change what the next test reads.
func slicesClone(in []Resource) []Resource {
	out := make([]Resource, len(in))
	copy(out, in)

	return out
}

// TestRun_RefusesALayerNameThatCouldLeaveTheTree is the check the annotation
// beside filepath.Join stands on.
//
// A layer name is joined onto the repository root, so a name that can express a
// separator can name a directory outside the tree. gosec's taint analysis asks
// about exactly this and cannot see that the regexp answers it — so the answer
// has to be a test.
func TestRun_RefusesALayerNameThatCouldLeaveTheTree(t *testing.T) {
	t.Parallel()

	for _, layer := range []string{
		"../escape", "..", "/etc", "30-cluster-services/../..",
		"30-cluster-services;rm", "Cluster-Services", "cluster-services", "3-short",
	} {
		err := run(t.Context(), []string{layer, "dev", "cert-manager"}, &strings.Builder{})

		require.Error(t, err, layer)
		assert.Contains(t, err.Error(), "is not a layer", layer)
	}
}

// TestRun_RefusesAStackNameTheCLIWouldNotTake, for the same reason one
// argument further along: --stack reaches an exec.Command, and the stack name
// is whatever an operator typed after `stack=`.
func TestRun_RefusesAStackNameTheCLIWouldNotTake(t *testing.T) {
	t.Parallel()

	for _, stack := range []string{"bad;name", "with space", ".leading-period", "a/b", ""} {
		err := run(t.Context(), []string{"30-cluster-services", stack, "cert-manager"}, &strings.Builder{})

		require.Error(t, err, stack)
		assert.Contains(t, err.Error(), "is not a stack name", stack)
	}
}

// TestRun_AcceptsTheNamesThisRepositoryActuallyUses keeps the two patterns from
// being tightened into refusing the real thing.
func TestRun_AcceptsTheNamesThisRepositoryActuallyUses(t *testing.T) {
	t.Parallel()

	for _, layer := range []string{
		"10-node-platform", "20-network-policy", "30-cluster-services", "40-ingress", "50-gitops",
	} {
		assert.True(t, LayerName.MatchString(layer), layer)
	}

	for _, stack := range []string{"dev", "prod", "platform-hel", "staging_2", "a.b"} {
		assert.True(t, StackName.MatchString(stack), stack)
	}
}

// TestMatchAll_ResolvesEverySelectorInOrder is the list form: one apply with
// several --target flags rather than one apply per component.
func TestMatchAll_ResolvesEverySelectorInOrder(t *testing.T) {
	t.Parallel()

	urns, err := MatchAll(stack, "cert-manager,traefik")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"urn:pulumi:dev::cluster-services::kubernetes:helm.sh/v3:Release::cert-manager",
		"urn:pulumi:dev::cluster-services::hetzner-iac:platform:Ingress$kubernetes:helm.sh/v3:" +
			"Release::traefik",
	}, urns, "the URNs come in selector order, each one once")
}

// TestMatchAll_ToleratesSurroundingSpace keeps `target=a, b` working, because
// a shell and a taskfile both make that easy to type.
func TestMatchAll_ToleratesSurroundingSpace(t *testing.T) {
	t.Parallel()

	spaced, err := MatchAll(stack, " cert-manager , traefik ")
	require.NoError(t, err)

	tight, err := MatchAll(stack, "cert-manager,traefik")
	require.NoError(t, err)

	assert.Equal(t, tight, spaced)
}

// TestMatchAll_DeduplicatesOverlappingSelectors: a group and one of its
// members is a legitimate pair, and Pulumi should not be handed the same
// --target twice.
func TestMatchAll_DeduplicatesOverlappingSelectors(t *testing.T) {
	t.Parallel()

	group, err := Match(stack, "group:Ingress")
	require.NoError(t, err)

	both, err := MatchAll(stack, "group:Ingress,traefik")
	require.NoError(t, err)

	assert.Equal(t, group, both,
		"traefik is already under group:Ingress, so the list adds no URN")
}

// TestMatchAll_RefusesTheWholeListWhenOneSelectorIsWrong is the important one.
//
// Resolving the good half would be an apply that reports success having done
// less than was asked — the same failure a --target matching nothing is, which
// is what this program exists to prevent.
func TestMatchAll_RefusesTheWholeListWhenOneSelectorIsWrong(t *testing.T) {
	t.Parallel()

	urns, err := MatchAll(stack, "cert-manager,cert-manger")
	require.Error(t, err)
	assert.Nil(t, urns)
	assert.Contains(t, err.Error(), "cert-manger", "the message names the selector that failed")
}

// TestMatchAll_RefusesAnEmptyElement catches `target=a,` and `target=a,,b`,
// which a shell produces from an unset variable and which would otherwise
// resolve to the rest of the list.
func TestMatchAll_RefusesAnEmptyElement(t *testing.T) {
	t.Parallel()

	for _, selectors := range []string{"cert-manager,", ",cert-manager", "cert-manager,,traefik"} {
		_, err := MatchAll(stack, selectors)
		require.Error(t, err, selectors)
		require.ErrorIs(t, err, ErrNoSelector, selectors)
	}
}

// TestMatchAll_RefusesARepeatedSelector: harmless to resolve, and a typo worth
// naming — nobody means to write one component twice.
func TestMatchAll_RefusesARepeatedSelector(t *testing.T) {
	t.Parallel()

	_, err := MatchAll(stack, "cert-manager,cert-manager")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "twice")
}

// TestMatchAll_OneSelectorAgreesWithMatch keeps the single-selector path, which
// is what every existing task passes, byte-identical to Match.
func TestMatchAll_OneSelectorAgreesWithMatch(t *testing.T) {
	t.Parallel()

	for _, selector := range []string{"cert-manager", "Release:traefik", "group:Ingress"} {
		one, err := Match(stack, selector)
		require.NoError(t, err, selector)

		all, allErr := MatchAll(stack, selector)
		require.NoError(t, allErr, selector)

		assert.Equal(t, one, all, selector)
	}
}
