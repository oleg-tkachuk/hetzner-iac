package main

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"sigs.k8s.io/yaml"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/imagepolicy"
)

// checkImages holds every image a chart renders to an entry in the image
// inventory, and the inventory to images some chart still renders.
//
// The inventory is what the admission policies are generated from: a signer
// for a repository that publishes signatures, a digest pin for one that does
// not. An image with no entry would reach the cluster with no policy at all,
// and the case worth catching is a chart upgrade that adds one — Renovate
// bumps the chart, and the new sidecar arrives unclassified.
func checkImages(ctx context.Context) int {
	inventory, err := imagepolicy.Load()
	if err != nil {
		fmt.Printf("MISS  %-14s %v\n", "images", err)

		return 1
	}

	failures := 0
	used := map[string]bool{}

	for _, key := range charts.Keys() {
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

		images, err := renderedImages(output)
		if err != nil {
			fmt.Printf("MISS  %-14s %v\n", key, err)

			failures++

			continue
		}

		for _, image := range images {
			entry, found, err := inventory.Lookup(image)

			switch {
			case err != nil:
				fmt.Printf("MISS  %-14s %v\n", key, err)

				failures++
			case !found:
				fmt.Printf("MISS  %-14s %s has no entry in %s\n", key, image, imagepolicy.File)

				failures++
			default:
				used[entry.Repository] = true
			}
		}
	}

	for _, repository := range inventory.Repositories() {
		if !used[repository] {
			fmt.Printf("MISS  %-14s %s is in %s and no chart renders it\n", "images", repository, imagepolicy.File)

			failures++
		}
	}

	if failures == 0 {
		fmt.Printf("ok    %-14s every rendered image is in %s\n", "images", imagepolicy.File)
	}

	return failures
}

// renderedImages is every image a chart's workloads run, containers and init
// containers both, sorted and without repeats.
func renderedImages(manifests []byte) ([]string, error) {
	docs, err := documents(manifests)
	if err != nil {
		return nil, fmt.Errorf("the rendered manifests could not be read: %w", err)
	}

	seen := map[string]bool{}

	for _, doc := range docs {
		if !slices.Contains(workloadKinds, doc.Kind) {
			continue
		}

		var workload podSpec
		if err := yaml.Unmarshal([]byte(doc.Text), &workload); err != nil {
			return nil, fmt.Errorf("%s/%s could not be read: %w", doc.Kind, doc.Name, err)
		}

		pod := workload.Spec.Template.Spec
		for _, c := range slices.Concat(pod.Containers, pod.InitContainers) {
			if c.Image != "" {
				seen[c.Image] = true
			}
		}
	}

	images := make([]string, 0, len(seen))
	for image := range seen {
		images = append(images, image)
	}

	sort.Strings(images)

	return images, nil
}
