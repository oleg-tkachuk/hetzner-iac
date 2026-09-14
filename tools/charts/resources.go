package main

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/charts"

	"sigs.k8s.io/yaml"
)

// unmeasuredCharts are the charts this check does not yet hold to the policy,
// and the reason is the same for all three: they are not deployed, so nothing
// has measured them.
//
// Numbers invented for a chart nobody is running is the worse failure. A limit
// guessed too low does not show up in review — it shows up the first time
// somebody deploys the chart, as an OOMKill during the deploy they were
// watching for something else.
//
// This list is expected to shrink. Deploy one of these, measure it with
// `kubectl top pods --containers`, put the numbers in its values template, and
// delete the line.
var unmeasuredCharts = map[string]string{
	"cert-manager": reasonUnmeasured,
	"traefik":      reasonUnmeasured,
	"argo-cd":      reasonUnmeasured,
}

// reasonUnmeasured is why each of those is skipped, in one place so the three
// cannot drift into three different explanations of the same thing.
const reasonUnmeasured = "not deployed; no measurement exists"

// The resource names this check reads. Kubernetes spells them, not this
// repository: a typo here reports every container as unbounded, or none.
const (
	resourceMemory = "memory"
	resourceCPU    = "cpu"
)

// container is the part of a rendered pod spec this check reads.
type container struct {
	Name      string `json:"name"`
	Resources struct {
		Requests map[string]string `json:"requests"`
		Limits   map[string]string `json:"limits"`
	} `json:"resources"`
}

// podSpec is a rendered workload, reduced to its containers.
type podSpec struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []container `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

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

		fmt.Printf("      set it in pkg/values/%s.yaml.tmpl, from `kubectl top pods --containers`\n", key)

		failures += len(problems)
	}

	return failures
}

// unboundedContainers returns one message per container that breaks the policy.
func unboundedContainers(manifests []byte) []string {
	var problems []string

	for _, doc := range strings.Split(string(manifests), "\n---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}

		var workload podSpec
		if err := yaml.Unmarshal([]byte(doc), &workload); err != nil {
			// A document this check cannot parse is not a finding: helm emits
			// comments, empty documents and kinds with no pod template.
			continue
		}

		if !slices.Contains(workloadKinds, workload.Kind) {
			continue
		}

		for _, c := range workload.Spec.Template.Spec.Containers {
			where := fmt.Sprintf("%s/%s container %s", workload.Kind, workload.Metadata.Name, c.Name)

			if c.Resources.Requests[resourceMemory] == "" {
				problems = append(problems, where+" has no memory request")
			}

			if c.Resources.Limits[resourceMemory] == "" {
				problems = append(problems, where+" has no memory limit")
			}

			if c.Resources.Limits[resourceCPU] != "" {
				problems = append(problems, where+" has a cpu limit, which this platform does not use")
			}
		}
	}

	return problems
}
