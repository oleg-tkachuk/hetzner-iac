// Command network-policy installs the cluster's Cilium policies.
//
// Next after the CNI, because Cilium is what enforces these: the custom
// resources do not exist until its CRDs do, and a policy applied where the
// dataplane cannot read it is a file with no effect.
//
// The layer is two things with different risk. The allow policies set
// `enableDefaultDeny: false`, so they only ever permit and are safe to apply
// to a running cluster — without that field a policy selecting an endpoint
// switches it to default-deny for the directions it mentions, which means
// there is no such thing here as an allow rule that changes nothing. The
// default-deny is separate, and behind a config key, because it is the only
// one that takes traffic away.
//
// Every rule came from Hubble rather than from a diagram. What that measured,
// and what it changed:
//
//   - `reserved:host -> pod` dominates the cluster's traffic — the kubelet
//     running probes. Deny it and every workload restarts for ever. It is the
//     first policy in the directory for that reason.
//   - every controller reaches the API as egress to `reserved:host:6443`,
//     because the cluster tier points Cilium at KubePrism on the node. A
//     policy author looking for an apiserver endpoint would not find one.
//   - the only pod-to-pod flows of substance are Loki's gateway to its single
//     binary, Alloy to that gateway, and Argo CD to its cache.
package main

import (
	"fmt"
	"strconv"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"

	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/yaml"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Manifest names the two halves of the directory.
const (
	// AllowManifests permit and never deny. Applying them changes no
	// existing traffic.
	AllowManifests = "manifests/[0-4]*.yaml"

	// DenyManifest is the one that takes traffic away.
	DenyManifest = "manifests/90-default-deny.yaml"
)

// EnabledKey is the stack config switch that applies the deny. Spelled once,
// here: the layer reads it and Pulumi.yaml declares it.
const EnabledKey = "enabled"

// Components are what this layer deploys.
//
// Both are Create components: these are custom resources, not charts, and
// internal/pkg/charts has nothing to pin for them.
var Components = layer.Components{
	{
		Name:   "allow",
		Create: createAllows,
	},
	{
		Name:   "default-deny",
		After:  []string{"allow"},
		Create: createDefaultDeny,
	},
}

func main() {
	layer.RunComponents(Components)
}

// denyRequested reads the switch.
//
// An unparseable value is an error rather than a silent false. For this flag
// the two states do not look different from outside: a cluster where the deny
// was never applied and a cluster where `enabled: yes` was ignored both show
// no deny policy, and the second one has an operator who believes otherwise.
func denyRequested(value string) (bool, error) {
	if value == "" {
		return false, nil
	}

	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf(
			"config %q is %q, which is not a boolean: set it to true or false", EnabledKey, value)
	}

	return enabled, nil
}

// createAllows applies every policy that only permits.
func createAllows(r *layer.Runner, dependencies []pulumi.Resource) (pulumi.Resource, error) {
	r.Log.Step("allow", "policies that permit and never deny")

	return yaml.NewConfigGroup(r.Ctx, "allow", &yaml.ConfigGroupArgs{
		Files: []string{AllowManifests},
	}, r.With(layer.DependsOn(dependencies)...)...)
}

// createDefaultDeny applies the deny, if this stack asks for it.
//
// Returning (nil, nil) is how the component declines, which keeps it in the
// set — still enumerated, still ordered after the allows — rather than hidden
// behind an `if` where no test can see it.
//
// Off by default because the allow rules describe the cluster as it is today,
// and three things it will need are not in them: cert-manager reaching Let's
// Encrypt, Argo CD fetching from git, and Alertmanager reaching a receiver.
// None exists yet, so none could be measured. Turning the deny on before they
// do would make the first of them fail in a way that reads as a broken
// component rather than as policy.
func createDefaultDeny(r *layer.Runner, dependencies []pulumi.Resource) (pulumi.Resource, error) {
	enabled, err := denyRequested(r.Cfg.Get(EnabledKey))
	if err != nil {
		return nil, err
	}

	if !enabled {
		r.Log.Skipped("default-deny", "not enabled, only the allow policies are applied")

		return nil, nil
	}

	r.Log.Warn("default-deny", "everything not named by an allow policy is now dropped")

	return yaml.NewConfigFile(r.Ctx, "default-deny", &yaml.ConfigFileArgs{
		File: DenyManifest,
	}, r.With(layer.DependsOn(dependencies)...)...)
}
