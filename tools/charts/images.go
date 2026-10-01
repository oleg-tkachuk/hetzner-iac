package main

import (
	"context"
	"fmt"
	"slices"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
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

// renderedImages is every image a chart's pods run — containers and init
// containers both, sorted and without repeats.
//
// Every kind that becomes a pod, not only the long-running workloads the
// render check asserts on. A hook Job runs once and finishes, which is why the
// render check ignores it, but admission sees its pod like any other:
// cert-manager's startupapicheck runs on every install, and an image only a
// Job uses would reach the cluster with no entry here.
func renderedImages(manifests []byte) ([]string, error) {
	docs, err := documents(manifests)
	if err != nil {
		return nil, fmt.Errorf("the rendered manifests could not be read: %w", err)
	}

	seen := map[string]bool{}

	for _, doc := range docs {
		pod, err := podOf(doc.Kind, []byte(doc.Text))
		if err != nil {
			return nil, fmt.Errorf("%s/%s could not be read: %w", doc.Kind, doc.Name, err)
		}

		if pod == nil {
			continue
		}

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

// podOf is the pod spec a document runs, and nil for a kind that runs none.
//
// The API's own types rather than a struct of the fields read here: a CronJob
// nests its pod three levels deeper than a Deployment, and the types are what
// says where.
func podOf(kind string, text []byte) (*corev1.PodSpec, error) {
	switch kind {
	case string(charts.Deployment):
		var o appsv1.Deployment
		return &o.Spec.Template.Spec, yaml.Unmarshal(text, &o)
	case string(charts.StatefulSet):
		var o appsv1.StatefulSet
		return &o.Spec.Template.Spec, yaml.Unmarshal(text, &o)
	case string(charts.DaemonSet):
		var o appsv1.DaemonSet
		return &o.Spec.Template.Spec, yaml.Unmarshal(text, &o)
	case kindJob:
		var o batchv1.Job
		return &o.Spec.Template.Spec, yaml.Unmarshal(text, &o)
	case kindCronJob:
		var o batchv1.CronJob
		return &o.Spec.JobTemplate.Spec.Template.Spec, yaml.Unmarshal(text, &o)
	case kindPod:
		var o corev1.Pod
		return &o.Spec, yaml.Unmarshal(text, &o)
	}

	return nil, nil
}

// The kinds that run a pod beyond the workloads charts.Kind names.
const (
	kindJob     = "Job"
	kindCronJob = "CronJob"
	kindPod     = "Pod"
)
