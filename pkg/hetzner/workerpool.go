package hetzner

import (
	"fmt"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumix"
	talosmachine "github.com/pulumiverse/pulumi-talos/sdk/go/talos/machine"
)

// WorkerPool is a set of identically-shaped worker nodes.
//
// Pools exist so that node shape is a property of a group rather than of the
// cluster: a GPU pool, a memory-heavy pool and a general pool differ in server
// type, labels and taints while sharing one control plane.
type WorkerPool struct {
	pulumi.ResourceState

	Servers []*hcloud.Server `pulumi:"-"`

	NodeNames pulumi.StringArrayOutput `pulumi:"nodeNames"`
}

// WorkerPoolArgs configures one pool.
type WorkerPoolArgs struct {
	ClusterName string
	PoolName    string
	// PoolIndex decides the pool's fixed slice of the node subnet. It must
	// stay stable across changes: reordering pools in the topology file would
	// otherwise renumber their addresses and replace every node.
	PoolIndex int

	Count      int
	ServerType string
	Location   string

	Addressing *Addressing

	ImageID   pulumi.StringInput
	NetworkID pulumi.IntInput

	Labels map[string]string
	Taints []string

	TalosVersion      string
	KubernetesVersion string
	ClusterPatch      pulumi.StringInput
	Endpoint          pulumi.StringInput

	MachineSecrets      talosmachine.MachineSecretsInput
	ClientConfiguration talosmachine.ClientConfigurationOutput

	PublicIPv4 bool

	// Bootstrap is the control plane's etcd bootstrap. Workers depend on it so
	// they do not try to join a cluster that does not exist yet.
	Bootstrap pulumi.Resource
}

// NewWorkerPool creates the pool's servers and joins them to the cluster.
func NewWorkerPool(ctx *pulumi.Context, name string, args *WorkerPoolArgs, opts ...pulumi.ResourceOption) (*WorkerPool, error) {
	if args == nil {
		return nil, fmt.Errorf("NewWorkerPool(%s): args must not be nil", name)
	}

	if args.PoolName == "" {
		return nil, fmt.Errorf("NewWorkerPool(%s): pool name is required", name)
	}

	if args.Addressing == nil {
		return nil, fmt.Errorf("NewWorkerPool(%s): addressing is required", name)
	}

	component := &WorkerPool{}
	if err := ctx.RegisterComponentResource(typeWorkerPool, name, component, opts...); err != nil {
		return nil, fmt.Errorf("register %s: %w", typeWorkerPool, err)
	}

	parent := pulumi.Parent(component)

	machineConfig := talosmachine.GetConfigurationOutput(ctx, talosmachine.GetConfigurationOutputArgs{
		ClusterName:       pulumi.String(args.ClusterName),
		ClusterEndpoint:   args.Endpoint,
		MachineType:       pulumi.String(machineTypeWorker),
		MachineSecrets:    args.MachineSecrets,
		ConfigPatches:     pulumi.StringArray{args.ClusterPatch},
		KubernetesVersion: optionalString(args.KubernetesVersion),
		TalosVersion:      optionalString(args.TalosVersion),
	})

	clientConfig := args.ClientConfiguration.ToClientConfigurationPtrOutput()

	nodeLabels := map[string]string{LabelPool: args.PoolName}
	for key, value := range args.Labels {
		nodeLabels[key] = value
	}

	servers := make([]*hcloud.Server, 0, args.Count)
	names := make([]string, 0, args.Count)

	for i := range args.Count {
		privateIP, err := args.Addressing.WorkerIP(args.PoolIndex, i)
		if err != nil {
			return nil, fmt.Errorf("worker pool %q node %d: %w", args.PoolName, i, err)
		}

		hostname := NodeName(args.ClusterName, args.PoolName, i)

		server, err := newServer(ctx, serverSpec{
			name:       hostname,
			serverType: args.ServerType,
			location:   args.Location,
			imageID:    args.ImageID,
			networkID:  args.NetworkID,
			privateIP:  privateIP,
			labels: ResourceLabels(args.ClusterName, map[string]string{
				LabelRole: RoleWorker,
				LabelPool: args.PoolName,
			}),
			publicIPv4: args.PublicIPv4,
		}, parent)
		if err != nil {
			return nil, err
		}

		address := nodeAddress(server, args.PublicIPv4, privateIP)

		patch := pulumix.Cast[pulumi.StringOutput](pulumix.ApplyErr(address,
			func(addr string) (string, error) {
				return BuildNodePatch(NodePatchArgs{
					Hostname:   hostname,
					CertSANs:   []string{addr, privateIP},
					NodeLabels: nodeLabels,
					NodeTaints: args.Taints,
				})
			}))

		dependencies := []pulumi.Resource{server}
		if args.Bootstrap != nil {
			dependencies = append(dependencies, args.Bootstrap)
		}

		if _, err := talosmachine.NewConfigurationApply(ctx, fmt.Sprintf("%s-config-%d", name, i),
			&talosmachine.ConfigurationApplyArgs{
				ClientConfiguration:       clientConfig,
				MachineConfigurationInput: machineConfig.MachineConfiguration(),
				Node:                      address,
				ConfigPatches:             pulumi.StringArray{patch},
			}, parent, pulumi.DependsOn(dependencies)); err != nil {
			return nil, fmt.Errorf("talos configuration apply for %s: %w", hostname, err)
		}

		servers = append(servers, server)
		names = append(names, hostname)
	}

	component.Servers = servers
	component.NodeNames = pulumi.ToStringArray(names).ToStringArrayOutput()

	if err := ctx.RegisterResourceOutputs(component, pulumi.Map{
		"nodeNames": component.NodeNames,
	}); err != nil {
		return nil, fmt.Errorf("register worker pool outputs: %w", err)
	}

	return component, nil
}
