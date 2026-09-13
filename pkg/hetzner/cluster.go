package hetzner

import (
	"fmt"
	"strconv"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumix"
	talosclient "github.com/pulumiverse/pulumi-talos/sdk/go/talos/client"
	talosmachine "github.com/pulumiverse/pulumi-talos/sdk/go/talos/machine"
)

// Cluster is a complete Talos Kubernetes cluster on Hetzner Cloud: the private
// network, the perimeter firewall, the control plane, the worker pools and the
// credentials to reach them.
//
// It stops at "a Kubernetes API that answers". No CNI is installed — the
// layers/10-node-platform owns that — so nodes stay NotReady until it runs. That
// intended handover point, not an incomplete cluster.
type Cluster struct {
	pulumi.ResourceState

	Network      *Network      `pulumi:"-"`
	Firewall     *Firewall     `pulumi:"-"`
	ControlPlane *ControlPlane `pulumi:"-"`
	WorkerPools  []*WorkerPool `pulumi:"-"`

	// Kubeconfig and Talosconfig are both cluster-admin credentials, hence
	// secret outputs. Talosconfig is the more powerful of the two: it can
	// reset nodes and read etcd.
	Kubeconfig  pulumi.StringOutput `pulumi:"kubeconfig"`
	Talosconfig pulumi.StringOutput `pulumi:"talosconfig"`

	Endpoint          pulumi.StringOutput `pulumi:"endpoint"`
	APILoadBalancerIP pulumi.StringOutput `pulumi:"apiLoadBalancerIp"`
	NetworkID         pulumi.IntOutput    `pulumi:"networkId"`
	PodCIDR           pulumi.StringOutput `pulumi:"podCidr"`
	ServiceCIDR       pulumi.StringOutput `pulumi:"serviceCidr"`
}

// ClusterArgs is the resolved topology plus anything that must not live in a
// committed file.
type ClusterArgs struct {
	Topology *Topology

	// ImageSelector overrides the label selector used to find the Talos
	// snapshot. Empty derives it from the Talos version, which is what
	// `task cluster:image-bake` labels the snapshot with.
	ImageSelector string

	// PublicIPv4 keeps a routable address on every node. Required unless the
	// apply runs from inside the private network, because Talos configuration
	// is pushed over the Talos API.
	PublicIPv4 bool

	// AllowICMP opens ping from the admin CIDRs.
	AllowICMP bool
}

// NewCluster builds the whole cluster.
func NewCluster(ctx *pulumi.Context, name string, args *ClusterArgs, opts ...pulumi.ResourceOption) (*Cluster, error) {
	if args == nil || args.Topology == nil {
		return nil, fmt.Errorf("NewCluster(%s): topology is required", name)
	}

	topology := args.Topology

	// Before anything is registered. A bad server type used to surface on the
	// eleventh resource, after ten had been created, leaving a half-built
	// cluster in state for the next run to reconcile.
	if err := ValidateServerTypes(ctx, topology); err != nil {
		return nil, err
	}

	component := &Cluster{}
	if err := ctx.RegisterComponentResource(typeCluster, name, component, opts...); err != nil {
		return nil, fmt.Errorf("register %s: %w", typeCluster, err)
	}

	parent := pulumi.Parent(component)

	addressing, err := NewAddressing(topology.Network.NodeSubnet, PoolAddressStride)
	if err != nil {
		return nil, err
	}

	network, err := NewNetwork(ctx, name+"-network", &NetworkArgs{
		ClusterName: topology.Metadata.Name,
		IPRange:     topology.Network.IPRange,
		NodeSubnet:  topology.Network.NodeSubnet,
		NetworkZone: topology.Placement.NetworkZone,
	}, parent)
	if err != nil {
		return nil, err
	}

	firewall, err := NewFirewall(ctx, name+"-firewall", &FirewallArgs{
		ClusterName: topology.Metadata.Name,
		AdminCIDRs:  topology.Network.AdminCIDRs,
		AllowICMP:   args.AllowICMP,
	}, parent)
	if err != nil {
		return nil, err
	}

	image := lookupTalosImage(ctx, args)

	// Talos secrets are the cluster's root of trust: the CA keys every node
	// and client certificate descends from. Protect stops a `pulumi destroy`
	// from taking them out from under a cluster that still exists.
	secrets, err := talosmachine.NewSecrets(ctx, name+"-secrets", &talosmachine.SecretsArgs{
		TalosVersion: pulumi.String(topology.Talos.Version),
	}, parent, pulumi.Protect(true))
	if err != nil {
		return nil, fmt.Errorf("talos secrets: %w", err)
	}

	etcdPatch, err := BuildEtcdPatch(topology.Network.NodeSubnet)
	if err != nil {
		return nil, err
	}

	clusterPatch, err := BuildClusterPatch(ClusterPatchArgs{
		PodCIDR:     topology.Network.PodCIDR,
		ServiceCIDR: topology.Network.ServiceCIDR,
		NodeSubnet:  topology.Network.NodeSubnet,
		// With no worker pool the control plane is the only place a pod can
		// run, so scheduling has to be allowed there or nothing starts.
		AllowSchedulingOnControlPlanes: totalWorkers(topology) == 0,
	})
	if err != nil {
		return nil, err
	}

	// Anti-affinity for the control plane only. It is what turns three etcd
	// members into three failure domains rather than three VMs that can share
	// one physical host. Worker pools skip it because a spread group holds at
	// most ten servers, which would cap pool size at an arbitrary number.
	placementGroup, err := hcloud.NewPlacementGroup(ctx, name+"-control-plane", &hcloud.PlacementGroupArgs{
		Name:   pulumi.Sprintf("%s-control-plane", topology.Metadata.Name),
		Type:   pulumi.String("spread"),
		Labels: toStringMap(ResourceLabels(topology.Metadata.Name, nil)),
	}, parent)
	if err != nil {
		return nil, fmt.Errorf("hcloud placement group: %w", err)
	}

	apiAddress, loadBalancerIP, err := apiEndpointAddress(ctx, name, topology, network, parent)
	if err != nil {
		return nil, err
	}

	controlPlane, err := NewControlPlane(ctx, name+"-control-plane", &ControlPlaneArgs{
		ClusterName:         topology.Metadata.Name,
		Count:               topology.ControlPlane.Count,
		ServerType:          topology.ControlPlane.ServerType,
		Location:            topology.Placement.Location,
		Addressing:          addressing,
		ImageID:             image,
		NetworkID:           network.NetworkID,
		PlacementGroupID:    idToIntPtr(placementGroup.ID()),
		APIAddress:          apiAddress,
		KubernetesVersion:   topology.Kubernetes.Version,
		TalosVersion:        topology.Talos.Version,
		ClusterPatch:        pulumi.String(clusterPatch),
		EtcdPatch:           pulumi.String(etcdPatch),
		MachineSecrets:      secrets.MachineSecrets,
		ClientConfiguration: secrets.ClientConfiguration,
		PublicIPv4:          args.PublicIPv4,
	}, parent,
		// The subnet must exist before a server can take an address in it, and
		// the firewall before a node is reachable — otherwise there is a window
		// where a control-plane node is up with no perimeter.
		pulumi.DependsOn([]pulumi.Resource{network.Subnet, firewall.Firewall}))
	if err != nil {
		return nil, err
	}

	pools := make([]*WorkerPool, 0, len(topology.WorkerPools))

	for i, spec := range topology.WorkerPools {
		pool, err := NewWorkerPool(ctx, fmt.Sprintf("%s-%s", name, spec.Name), &WorkerPoolArgs{
			ClusterName:         topology.Metadata.Name,
			PoolName:            spec.Name,
			PoolIndex:           i,
			Count:               spec.Count,
			ServerType:          spec.ServerType,
			Location:            topology.Placement.Location,
			Addressing:          addressing,
			ImageID:             image,
			NetworkID:           network.NetworkID,
			Labels:              spec.Labels,
			Taints:              spec.Taints,
			TalosVersion:        topology.Talos.Version,
			KubernetesVersion:   topology.Kubernetes.Version,
			ClusterPatch:        pulumi.String(clusterPatch),
			Endpoint:            controlPlane.Endpoint,
			MachineSecrets:      secrets.MachineSecrets,
			ClientConfiguration: secrets.ClientConfiguration,
			PublicIPv4:          args.PublicIPv4,
			Bootstrap:           controlPlane.Bootstrap,
		}, parent, pulumi.DependsOn([]pulumi.Resource{network.Subnet, firewall.Firewall}))
		if err != nil {
			return nil, err
		}

		pools = append(pools, pool)
	}

	talosconfig := talosclient.GetConfigurationOutput(ctx, talosclient.GetConfigurationOutputArgs{
		ClusterName: pulumi.String(topology.Metadata.Name),
		ClientConfiguration: talosclient.GetConfigurationClientConfigurationArgs{
			CaCertificate:     secrets.ClientConfiguration.CaCertificate(),
			ClientCertificate: secrets.ClientConfiguration.ClientCertificate(),
			ClientKey:         secrets.ClientConfiguration.ClientKey(),
		},
		Endpoints: pulumi.StringArray{controlPlane.FirstNodeAddress},
		Nodes:     pulumi.StringArray{controlPlane.FirstNodeAddress},
	})

	component.Network = network
	component.Firewall = firewall
	component.ControlPlane = controlPlane
	component.WorkerPools = pools
	component.Kubeconfig = controlPlane.Kubeconfig
	component.Talosconfig = pulumi.ToSecret(talosconfig.TalosConfig()).(pulumi.StringOutput)
	component.Endpoint = controlPlane.Endpoint
	component.APILoadBalancerIP = loadBalancerIP
	component.NetworkID = network.NetworkID
	component.PodCIDR = pulumi.String(topology.Network.PodCIDR).ToStringOutput()
	component.ServiceCIDR = pulumi.String(topology.Network.ServiceCIDR).ToStringOutput()

	if err := ctx.RegisterResourceOutputs(component, pulumi.Map{
		"kubeconfig":        component.Kubeconfig,
		"talosconfig":       component.Talosconfig,
		"endpoint":          component.Endpoint,
		"apiLoadBalancerIp": component.APILoadBalancerIP,
		"networkId":         component.NetworkID,
		"podCidr":           component.PodCIDR,
		"serviceCidr":       component.ServiceCIDR,
	}); err != nil {
		return nil, fmt.Errorf("register cluster outputs: %w", err)
	}

	return component, nil
}

// apiEndpointAddress returns the address the cluster endpoint points at, and
// the load-balancer IP when there is one.
//
// A single control plane gets no load balancer: there is nothing to fail over
// between, so one would only cost money. The endpoint then resolves to the
// node's own address, which is why replacing a single control-plane node
// changes the endpoint — an accepted property of a non-HA cluster.
func apiEndpointAddress(
	ctx *pulumi.Context,
	name string,
	topology *Topology,
	network *Network,
	opts ...pulumi.ResourceOption,
) (pulumi.StringInput, pulumi.StringOutput, error) {
	if topology.ControlPlane.Count == 1 {
		return nil, pulumi.String("").ToStringOutput(), nil
	}

	loadBalancer, err := hcloud.NewLoadBalancer(ctx, name+"-api", &hcloud.LoadBalancerArgs{
		Name:             pulumi.Sprintf("%s-api", topology.Metadata.Name),
		LoadBalancerType: pulumi.String(topology.ControlPlane.APILoadBalancerType),
		Location:         pulumi.String(topology.Placement.Location),
		Labels:           toStringMap(ResourceLabels(topology.Metadata.Name, nil)),
	}, opts...)
	if err != nil {
		return nil, pulumi.StringOutput{}, fmt.Errorf("hcloud api load balancer: %w", err)
	}

	// Kept, not discarded: the target below cannot exist until this has.
	attachment, err := hcloud.NewLoadBalancerNetwork(ctx, name+"-api-network", &hcloud.LoadBalancerNetworkArgs{
		LoadBalancerId: idToInt(loadBalancer.ID()),
		NetworkId:      network.NetworkID,
	}, append(opts, pulumi.DependsOn([]pulumi.Resource{network.Subnet}))...)
	if err != nil {
		return nil, pulumi.StringOutput{}, fmt.Errorf("hcloud api load balancer network: %w", err)
	}

	if _, err := hcloud.NewLoadBalancerService(ctx, name+"-api-service", &hcloud.LoadBalancerServiceArgs{
		LoadBalancerId:  loadBalancer.ID().ToStringOutput(),
		Protocol:        pulumi.String("tcp"),
		ListenPort:      pulumi.Int(PortKubeAPI),
		DestinationPort: pulumi.Int(PortKubeAPI),
		HealthCheck: &hcloud.LoadBalancerServiceHealthCheckArgs{
			Protocol: pulumi.String("tcp"),
			Port:     pulumi.Int(PortKubeAPI),
			Interval: pulumi.Int(10),
			Timeout:  pulumi.Int(5),
			Retries:  pulumi.Int(3),
		},
	}, opts...); err != nil {
		return nil, pulumi.StringOutput{}, fmt.Errorf("hcloud api load balancer service: %w", err)
	}

	// Targets are selected by label so a replaced control-plane node is picked
	// up without a diff on the load balancer.
	//
	// DependsOn the network ATTACHMENT, not just the subnet. UsePrivateIp is
	// what makes that ordering load-bearing: Hetzner refuses a private-address
	// target on a load balancer that is not in a network yet, and with only the
	// subnet in the list Pulumi is free to create the two in parallel. It did,
	// and the first real HA apply this repository ever ran failed with
	//
	//   add label selector target: load balancer is not attached to a network
	//   (load_balancer_not_attached_to_network)
	//
	// after creating everything else — which is the shape of bug a commented
	// configuration hides: the code was never wrong anywhere a test could see.
	if _, err := hcloud.NewLoadBalancerTarget(ctx, name+"-api-targets", &hcloud.LoadBalancerTargetArgs{
		LoadBalancerId: idToInt(loadBalancer.ID()),
		Type:           pulumi.String("label_selector"),
		LabelSelector: pulumi.String(fmt.Sprintf("%s,%s=%s",
			ClusterSelector(topology.Metadata.Name), LabelRole, RoleControlPlane)),
		UsePrivateIp: pulumi.Bool(true),
	}, append(opts, pulumi.DependsOn([]pulumi.Resource{network.Subnet, attachment}))...); err != nil {
		return nil, pulumi.StringOutput{}, fmt.Errorf("hcloud api load balancer target: %w", err)
	}

	return loadBalancer.Ipv4, loadBalancer.Ipv4, nil
}

// lookupTalosImage resolves the snapshot the nodes boot from.
//
// Pulumi only LOOKS the snapshot up. Hetzner has no custom-image upload API —
// an image becomes usable only as a snapshot taken from a rescue-booted
// server — so baking is a deliberate out-of-band step (`task cluster:image-bake`).
// Doing it inside the stack would mean dd-over-SSH in the provisioning path,
// re-run on every unrelated `pulumi up`.
func lookupTalosImage(ctx *pulumi.Context, args *ClusterArgs) pulumi.StringOutput {
	selector := args.ImageSelector
	if selector == "" {
		selector = fmt.Sprintf("os=talos,talos-version=%s", args.Topology.Talos.Version)
	}

	image := hcloud.GetImageOutput(ctx, hcloud.GetImageOutputArgs{
		WithSelector:     pulumi.String(selector),
		WithArchitecture: pulumi.String(args.Topology.Talos.Architecture),
		WithStatuses:     pulumi.ToStringArray([]string{"available"}),
		MostRecent:       pulumi.Bool(true),
	})

	// Failing here is deliberate. Falling back to a stock OS image would build
	// a cluster of Debian nodes that join nothing, and the failure would only
	// surface much later as a cluster that never bootstraps.
	return pulumix.Cast[pulumi.StringOutput](pulumix.ApplyErr(image.Id(), func(id *int) (string, error) {
		if id == nil || *id == 0 {
			return "", fmt.Errorf("no available Talos snapshot matches selector %q for architecture %s — run `task cluster:image-bake`",
				selector, args.Topology.Talos.Architecture)
		}

		return strconv.Itoa(*id), nil
	}))
}

func idToIntPtr(id pulumi.IDOutput) pulumi.IntPtrInput {
	return idToInt(id).ToIntPtrOutput()
}

func totalWorkers(topology *Topology) int {
	total := 0
	for _, pool := range topology.WorkerPools {
		total += pool.Count
	}

	return total
}
