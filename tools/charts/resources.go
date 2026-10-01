package main

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"

	"sigs.k8s.io/yaml"
)

// unmeasuredCharts are the charts this check does not yet hold to the policy,
// because no measured numbers for them are committed.
//
// Numbers invented for a chart is the worse failure. A limit guessed too low
// does not show up in review — it shows up the first time somebody deploys the
// chart, as an OOMKill during the deploy they were watching for something
// else.
//
// The reason is deliberately about THIS REPOSITORY and not about the cluster.
// It first read "not deployed; no measurement exists", and that was a claim no
// test could check: cert-manager sat on this list while `task platform:apply
// stack=dev layer=30-cluster-services` was installing it, and the three
// containers it left unbounded were found by looking at the cluster, not by
// any gate here. Whether a chart is deployed depends on which stacks somebody
// has applied, which this code cannot see. Whether its numbers are committed
// is visible in the values template beside it — so that is what the skip says,
// and TestUnmeasuredCharts_HaveNoMeasurementsToUse checks it.
//
// This list is expected to shrink. Deploy one of these, measure it with
// `kubectl top pods --containers`, put the numbers in its values template, and
// delete the line.
var unmeasuredCharts = map[string]string{
	// argo-cd only, and it stays for a reason narrower than the one above:
	// nine components are deployed and idle, because this Argo CD reconciles
	// nothing. Measuring it now would repeat the mistake traefik's first
	// measurement made — repo-server is the component that grows, and it grows
	// while rendering manifests, which it has never done. Measure it after the
	// first real sync.
	"argo-cd": reasonUnmeasured,
}

// reasonUnmeasured is why each of those is skipped, in one place so they
// cannot drift into different explanations of the same thing.
const reasonUnmeasured = "no measured numbers committed for it"

// The resource names this check reads. Kubernetes spells them, not this
// repository: a typo here reports every container as unbounded, or none.
const (
	resourceMemory = "memory"
	resourceCPU    = "cpu"
)

// container is the part of a rendered pod spec this check reads.
type container struct {
	Name      string `json:"name"`
	Image     string `json:"image"`
	Resources struct {
		Requests map[string]string `json:"requests"`
		Limits   map[string]string `json:"limits"`
	} `json:"resources"`
}

// podSpec is a rendered workload, reduced to its containers.
//
// Init containers too. One runs on the same node as the rest and takes memory
// from the same place, and one with `restartPolicy: Always` is a sidecar that
// runs for the pod's whole life — the shape a chart upgrade adds a sidecar in.
type podSpec struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers     []container `json:"containers"`
				InitContainers []container `json:"initContainers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

// How a finding names the list a container came from.
const (
	roleContainer     = "container"
	roleInitContainer = "init container"
)

// checkResources proves that every container this platform installs is bounded.
//
// Three rules, and each has a failure behind it:
//
//   - a memory REQUEST, because without one the scheduler will overcommit a
//     node it has no reason to think is full;
//   - a memory LIMIT, because a request bounds scheduling and nothing else: a
//     container with a 32Mi request and no limit can grow to the whole node,
//     and on this platform that node also runs etcd and kube-apiserver;
//   - NO CPU limit, because a node has two cores and throttling the control
//     plane's neighbours costs latency to bound what requests already bound.
//
// Structural rather than a substring, which is what `Effects` does for the
// settings whose misspelling is silent. A substring cannot notice a container
// that a chart upgrade ADDS — and that is the case worth catching, because a
// new sidecar arrives unbounded and nothing says so.
func checkResources(ctx context.Context) int {
	failures := 0

	for _, key := range charts.Keys() {
		if reason, skip := unmeasuredCharts[key]; skip {
			fmt.Printf("skip  %-14s resources: %s\n", key, reason)

			continue
		}

		chart, err := charts.Get(key)
		if err != nil {
			fmt.Printf("MISS  %-14s unknown chart\n", key)

			failures++

			continue
		}

		output, err := renderRaw(ctx, chart, key, chart.Namespace, key)
		if err != nil {
			fmt.Printf("MISS  %-14s render failed: %v\n", key, err)

			failures++

			continue
		}

		problems := unboundedContainers(output)
		if len(problems) == 0 {
			fmt.Printf("ok    %-14s every container has requests and a memory limit\n", key)

			continue
		}

		sort.Strings(problems)

		for _, problem := range problems {
			fmt.Printf("MISS  %-14s %s\n", key, problem)
		}

		fmt.Printf("      set it in %s/%s%s, from `kubectl top pods --containers`\n", charts.Dir, key, charts.Extension)

		failures += len(problems)
	}

	return failures
}

// unboundedContainers returns one message per container that breaks the policy.
//
// A document that does not parse is a finding, not a skip. Helm's comments and
// empty documents are handled by documents(), which splits the way kubectl
// does; what is left failing is a manifest this check could not read, and
// passing it would pass every container in it unexamined.
func unboundedContainers(manifests []byte) []string {
	docs, err := documents(manifests)
	if err != nil {
		return []string{fmt.Sprintf("the rendered manifests could not be read: %v", err)}
	}

	var problems []string

	for _, doc := range docs {
		if !slices.Contains(workloadKinds, doc.Kind) {
			continue
		}

		var workload podSpec
		if err := yaml.Unmarshal([]byte(doc.Text), &workload); err != nil {
			problems = append(problems, fmt.Sprintf("%s/%s could not be read: %v", doc.Kind, doc.Name, err))

			continue
		}

		pod := workload.Spec.Template.Spec

		for role, list := range map[string][]container{
			roleContainer:     pod.Containers,
			roleInitContainer: pod.InitContainers,
		} {
			for _, c := range list {
				where := fmt.Sprintf("%s/%s %s %s", workload.Kind, workload.Metadata.Name, role, c.Name)
				problems = append(problems, containerProblems(where, c)...)
			}
		}
	}

	return problems
}

// containerProblems is the policy for one container, wherever it was listed.
func containerProblems(where string, c container) []string {
	var problems []string

	if c.Resources.Requests[resourceMemory] == "" {
		problems = append(problems, where+" has no memory request")
	}

	if c.Resources.Limits[resourceMemory] == "" {
		problems = append(problems, where+" has no memory limit")
	}

	if c.Resources.Limits[resourceCPU] != "" {
		problems = append(problems, where+" has a cpu limit, which this platform does not use")
	}

	return problems
}
