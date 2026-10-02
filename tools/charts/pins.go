package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/distribution/reference"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/imagepolicy"
)

// defaultRenderSets enables, for the default render, the components this
// platform turns on and the chart leaves off — without them an unsigned image
// the platform runs would be absent from the render its tag is read from.
var defaultRenderSets = map[string][]string{
	// Hubble UI is off in Cilium's defaults and on in this platform's values.
	charts.Cilium: {"hubble.ui.enabled=true", "hubble.relay.enabled=true"},
}

// checkPins holds every unsigned pin to the tag its chart renders by default.
//
// A pin is a tag and the digest it resolved to. When Renovate bumps the chart
// its default tag moves and the pin does not, and the values file keeps
// handing the chart the old digest — so the cluster runs the previous release
// with every other check green. This renders each chart on its own defaults
// and fails until the pin is moved with it.
func checkPins(ctx context.Context) int {
	inventory, err := imagepolicy.Load()
	if err != nil {
		fmt.Printf("MISS  %-14s %v\n", "pins", err)

		return 1
	}

	failures := 0
	seen := map[string]bool{}

	for _, key := range charts.Keys() {
		chart, err := charts.Get(key)
		if err != nil {
			fmt.Printf("MISS  %-14s unknown chart\n", key)

			failures++

			continue
		}

		images, err := defaultImages(ctx, key, chart)
		if err != nil {
			fmt.Printf("MISS  %-14s %v\n", key, err)

			failures++

			continue
		}

		problems, checked := stalePins(images, inventory)
		for repository := range checked {
			seen[repository] = true
		}

		for _, problem := range problems {
			fmt.Printf("MISS  %-14s %s\n", key, problem)
		}

		failures += len(problems)
	}

	for _, problem := range uncheckedPins(inventory, seen) {
		fmt.Printf("MISS  %-14s %s\n", "pins", problem)

		failures++
	}

	if failures == 0 {
		fmt.Printf("ok    %-14s every digest pin is for the tag its chart renders\n", "pins")
	}

	return failures
}

// defaultImages is every image a chart renders on its own defaults — no values
// file — which is where the tag a pin must follow comes from.
func defaultImages(ctx context.Context, key string, chart charts.Chart) ([]string, error) {
	output, err := renderRaw(ctx, chart, key, chart.Namespace, "", defaultRenderSets[key]...)
	if err != nil {
		return nil, fmt.Errorf("default render failed: %w", err)
	}

	return renderedImages(output)
}

// stalePins compares the images a chart renders on its defaults with the
// inventory's pins, and returns one message per pin whose tag the chart has
// moved past, with the repositories it was able to check.
func stalePins(images []string, inventory *imagepolicy.Inventory) ([]string, map[string]bool) {
	var problems []string

	checked := map[string]bool{}

	for _, image := range images {
		entry, found, err := inventory.Lookup(image)
		if err != nil {
			problems = append(problems, err.Error())

			continue
		}

		if !found || entry.Unsigned == nil {
			continue
		}

		checked[entry.Repository] = true

		tag := tagOf(image)
		if tag != entry.Unsigned.Tag {
			problems = append(problems, fmt.Sprintf(
				"%s: the chart now renders tag %q and %s pins %q — set the new tag and `crane digest %s:%s` there",
				entry.Repository, tag, imagepolicy.File, entry.Unsigned.Tag, entry.Repository, tag))
		}
	}

	sort.Strings(problems)

	return problems, checked
}

// uncheckedPins is every pin no default render showed, whose tag therefore
// went unchecked — a component the chart renders only under values this
// platform sets, which defaultRenderSets has to turn on.
func uncheckedPins(inventory *imagepolicy.Inventory, seen map[string]bool) []string {
	var problems []string

	for _, entry := range inventory.Images {
		if entry.Unsigned != nil && !seen[entry.Repository] {
			problems = append(problems, fmt.Sprintf(
				"%s is pinned but no chart renders it on its defaults, so its tag cannot be checked — add the values that enable it to defaultRenderSets",
				entry.Repository))
		}
	}

	return problems
}

// tagOf is an image reference's tag, or empty when it has none.
func tagOf(image string) string {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return ""
	}

	if tagged, ok := named.(reference.Tagged); ok {
		return tagged.Tag()
	}

	return ""
}
