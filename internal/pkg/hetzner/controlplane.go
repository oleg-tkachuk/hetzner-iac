package hetzner

import (
	"fmt"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumix"
	taloscluster "github.com/pulumiverse/pulumi-talos/sdk/go/talos/cluster"
	talosmachine "github.com/pulumiverse/pulumi-talos/sdk/go/talos/machine"
)

const (
	machineTypeControlPlane = "controlplane"
	machineTypeWorker       = "worker"
)

// ControlPlane is the set of control-plane nodes plus the Talos bootstrap
// that turns them into a cluster.
type ControlPlane struct {
	pulumi.ResourceState

	Servers   []*hcloud.Server        `pulumi:"-"`
	Bootstrap *talosmachine.Bootstrap `pulumi:"-"`

	// Endpoint is the kube-apiserver URL clients use.
	Endpoint pulumi.StringOutput `pulumi:"endpoint"`
	// FirstNodeAddress is where talosctl and the kubeconfig resource connect.
	FirstNodeAddress pulumi.StringOutput `pulumi:"firstNodeAddress"`
	// Kubeconfig is as powerful as cluster-admin, so it is a secret output.
	Kubeconfig pulumi.StringOutput `pulumi:"kubeconfig"`
}

// ControlPlaneArgs configures the control-plane node set.
type ControlPlaneArgs struct {
	ClusterName string
	Count       int
	ServerType  string
	Location    string

	Addressing *Addressing

	ImageID          pulumi.StringInput
	NetworkID        pulumi.IntInput
	PlacementGroupID pulumi.IntPtrInput

	// APIAddress is the stable address in front of the control plane — the
	// load balancer, when there is more than one node to fail over between.
	// It is also signed into every node's kube-apiserver certificate.
	//
	// Nil means "use the first node's address", which is correct for a single
	// control plane and wrong the moment that node is replaced.
	APIAddress pulumi.StringInput

	// EtcdPatch pins etcd's peers to the private network. Control planes
	// only: Talos refuses the section on a worker, so it cannot ride along in
	// ClusterPatch, which both roles share.
	EtcdPatch pulumi.StringInput

	KubernetesVersion string
	TalosVersion      string
	ClusterPatch      pulumi.StringInput

	MachineSecrets      talosmachine.MachineSecretsInput
	ClientConfiguration talosmachine.ClientConfigurationOutput

	PublicIPv4 bool
}

// controlPlaneNode pairs a server with the addressing derived for it, so the
// create pass and the configure pass cannot disagree about which is which.
type controlPlaneNode struct {
	hostname  string
	server    *hcloud.Server
	address   pulumi.StringOutput
	privateIP string
}

// NewControlPlane creates the control-plane servers, applies their Talos
// machine configuration, bootstraps etcd and retrieves the kubeconfig.
func NewControlPlane(ctx *pulumi.Context, name string, args *ControlPlaneArgs, opts ...pulumi.ResourceOption) (*ControlPlane, error) {
	if args == nil {
		return nil, fmt.Errorf("NewControlPlane(%s): args must not be nil", name)
	}

	if args.Count < 1 || args.Count%2 == 0 {
		return nil, fmt.Errorf("control plane count %d must be odd and positive", args.Count)
	}

	if args.Addressing == nil {
		return nil, fmt.Errorf("NewControlPlane(%s): addressing is required", name)
	}

	component := &ControlPlane{}
	if err := ctx.RegisterComponentResource(typeControlPlane, name, component, opts...); err != nil {
		return nil, fmt.Errorf("register %s: %w", typeControlPlane, err)
	}

	parent := pulumi.Parent(component)

	nodes, err := createControlPlaneNodes(ctx, args, parent)
	if err != nil {
		return nil, err
	}

	// The endpoint is the load balancer on an HA cluster, and otherwise the
	// single node's own address — which is only knowable after the server
	// exists. Talos tolerates that ordering: a node boots into maintenance
	// mode and waits for a configuration to arrive.
	apiAddress := args.APIAddress
	if apiAddress == nil {
		apiAddress = nodes[0].address
	}

	endpoint := pulumi.Sprintf("https://%s:%d", apiAddress, PortKubeAPI)

	machineConfig := talosmachine.GetConfigurationOutput(ctx, talosmachine.GetConfigurationOutputArgs{
		ClusterName:       pulumi.String(args.ClusterName),
		ClusterEndpoint:   endpoint,
		MachineType:       pulumi.String(machineTypeControlPlane),
		MachineSecrets:    args.MachineSecrets,
		ConfigPatches:     pulumi.StringArray{args.ClusterPatch, args.EtcdPatch},
		KubernetesVersion: optionalString(args.KubernetesVersion),
		TalosVersion:      optionalString(args.TalosVersion),
	})

	clientConfig := args.ClientConfiguration.ToClientConfigurationPtrOutput()

	applies := make([]pulumi.Resource, 0, len(nodes))

	for i, node := range nodes {
		patch := controlPlaneNodePatch(node, apiAddress)

		apply, applyErr := talosmachine.NewConfigurationApply(ctx, fmt.Sprintf("%s-config-%d", name, i),
			&talosmachine.ConfigurationApplyArgs{
				ClientConfiguration:       clientConfig,
				MachineConfigurationInput: machineConfig.MachineConfiguration(),
				Node:                      node.address,
				ConfigPatches:             pulumi.StringArray{patch},
			}, parent, pulumi.DependsOn([]pulumi.Resource{node.server}))
		if applyErr != nil {
			return nil, fmt.Errorf("talos configuration apply for %s: %w", node.hostname, applyErr)
		}

		applies = append(applies, apply)
	}

	// The bootstrap runs exactly once, on the first node, and only after EVERY
	// control plane has its configuration. Bootstrapping while a peer is still
	// unconfigured leaves a one-member etcd the cluster never recovers from.
	//
	// ReplaceOnChanges on the node, because a bootstrap is a one-shot action
	// and the provider declares `node` as an updatable field. Replacing the
	// first control-plane server therefore produced an UPDATE here: Pulumi
	// rewrote the address in state and ran nothing, and the new node was left
	// saying
	//
	//     etcd is waiting to join the cluster, if this node is the first node
	//     in the cluster, please run `talosctl bootstrap`
	//
	// while the apply reported `3 to update, 1 to replace` and exited zero.
	// That is the worst shape a failure can take here — the tool says done
	// and the cluster has no etcd. A replacement runs the create, and the
	// create is the bootstrap.
	//
	// The delete half is a no-op: there is no un-bootstrapping, and the
	// provider returned in half a second when this was forced by hand.
	bootstrap, err := talosmachine.NewBootstrap(ctx, name+"-bootstrap", &talosmachine.BootstrapArgs{
		ClientConfiguration: clientConfig,
		Node:                nodes[0].address,
	}, parent, pulumi.DependsOn(applies), pulumi.ReplaceOnChanges([]string{"node"}))
	if err != nil {
		return nil, fmt.Errorf("talos bootstrap: %w", err)
	}

	kubeconfig, err := taloscluster.NewKubeconfig(ctx, name+"-kubeconfig", &taloscluster.KubeconfigArgs{
		ClientConfiguration: taloscluster.KubeconfigClientConfigurationArgs{
			CaCertificate:     args.ClientConfiguration.CaCertificate(),
			ClientCertificate: args.ClientConfiguration.ClientCertificate(),
			ClientKey:         args.ClientConfiguration.ClientKey(),
		},
		Node: nodes[0].address,
	}, parent, pulumi.DependsOn([]pulumi.Resource{bootstrap}))
	if err != nil {
		return nil, fmt.Errorf("talos kubeconfig: %w", err)
	}

	servers := make([]*hcloud.Server, 0, len(nodes))
	for _, node := range nodes {
		servers = append(servers, node.server)
	}

	component.Servers = servers
	component.Bootstrap = bootstrap
	component.Endpoint = endpoint
	component.FirstNodeAddress = nodes[0].address
	component.Kubeconfig = pulumi.ToSecret(kubeconfig.KubeconfigRaw).(pulumi.StringOutput)

	if err := ctx.RegisterResourceOutputs(component, pulumi.Map{
		"endpoint":         component.Endpoint,
		"firstNodeAddress": component.FirstNodeAddress,
		"kubeconfig":       component.Kubeconfig,
	}); err != nil {
		return nil, fmt.Errorf("register control plane outputs: %w", err)
	}

	return component, nil
}

func createControlPlaneNodes(ctx *pulumi.Context, args *ControlPlaneArgs, opts ...pulumi.ResourceOption) ([]controlPlaneNode, error) {
	nodes := make([]controlPlaneNode, 0, args.Count)

	for i := range args.Count {
		privateIP, err := args.Addressing.ControlPlaneIP(i)
		if err != nil {
			return nil, fmt.Errorf("control-plane %d: %w", i, err)
		}

		hostname := NodeName(args.ClusterName, RoleControlPlane, i)

		server, err := newServer(ctx, serverSpec{
			name:             hostname,
			serverType:       args.ServerType,
			location:         args.Location,
			imageID:          args.ImageID,
			networkID:        args.NetworkID,
			privateIP:        privateIP,
			placementGroupID: args.PlacementGroupID,
			labels: ResourceLabels(args.ClusterName, map[string]string{
				LabelRole: RoleControlPlane,
				LabelPool: RoleControlPlane,
			}),
			publicIPv4: args.PublicIPv4,
		}, opts...)
		if err != nil {
			return nil, err
		}

		nodes = append(nodes, controlPlaneNode{
			hostname:  hostname,
			server:    server,
			address:   nodeAddress(server, args.PublicIPv4, privateIP),
			privateIP: privateIP,
		})
	}

	return nodes, nil
}

// controlPlaneNodePatch renders one node's patch once its address is known.
//
// Apply2Err takes both outputs as TYPED arguments. The pulumi.All form it
// replaces hands the callback a []any and leaves the two addresses to be
// recovered by position — swap the indices and it still compiles, still runs,
// and quietly signs the wrong certificate SAN.
func controlPlaneNodePatch(node controlPlaneNode, apiAddress pulumi.StringInput) pulumi.StringOutput {
	return pulumix.Cast[pulumi.StringOutput](pulumix.Apply2Err(
		node.address, apiAddress.ToStringOutput(),
		func(address, endpointAddress string) (string, error) {
			return BuildNodePatch(NodePatchArgs{
				Hostname: node.hostname,
				CertSANs: []string{address, node.privateIP, endpointAddress},
			})
		}))
}

func optionalString(value string) pulumi.StringPtrInput {
	if value == "" {
		return nil
	}

	return pulumi.StringPtr(value)
}
