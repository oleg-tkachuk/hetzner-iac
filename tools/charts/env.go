package main

import (
	"fmt"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

// envContainer is the part of a rendered container this check reads.
type envContainer struct {
	Name string `json:"name"`
	Env  []struct {
		Name string `json:"name"`
	} `json:"env"`
}

// envPod is a rendered workload, reduced to its containers' environments.
type envPod struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers     []envContainer `json:"containers"`
				InitContainers []envContainer `json:"initContainers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

// checkDuplicateEnv refuses a chart that renders one variable twice in one
// container.
//
// Kubernetes accepts the duplicate and the later entry wins, so the values
// file says one thing and the pod runs with another, with nothing to say so.
// It happens when a values template sets a variable under `env` that the chart
// also writes from a key of its own — hcloud-ccm's HCLOUD_NETWORK, which it
// derives from networking.network — and the chart's copy, appended last, is
// the one the container sees.
func checkDuplicateEnv(key string, manifests []byte) error {
	found, err := duplicateEnv(manifests)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}

	if len(found) == 0 {
		return nil
	}

	return fmt.Errorf("%s renders an environment variable twice, and the later one wins:\n  %s\n"+
		"the chart sets it from a key of its own — set that key instead",
		key, strings.Join(found, "\n  "))
}

// duplicateEnv returns one message per container that names a variable more
// than once, per workload kind this check tracks.
func duplicateEnv(manifests []byte) ([]string, error) {
	docs, err := documents(manifests)
	if err != nil {
		return nil, err
	}

	var found []string

	for _, doc := range docs {
		if !slices.Contains(workloadKinds, doc.Kind) {
			continue
		}

		var pod envPod
		if err := yaml.Unmarshal([]byte(doc.Text), &pod); err != nil {
			return nil, fmt.Errorf("read %s/%s: %w", doc.Kind, doc.Name, err)
		}

		spec := pod.Spec.Template.Spec

		for _, c := range append(spec.InitContainers, spec.Containers...) {
			counts := map[string]int{}

			for _, variable := range c.Env {
				counts[variable.Name]++
			}

			for _, name := range sortedNames(counts) {
				if counts[name] > 1 {
					found = append(found, fmt.Sprintf("%s/%s container %s sets %s %d times",
						pod.Kind, pod.Metadata.Name, c.Name, name, counts[name]))
				}
			}
		}
	}

	return found, nil
}

// sortedNames keeps the message in the same order from one run to the next.
func sortedNames(counts map[string]int) []string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}
