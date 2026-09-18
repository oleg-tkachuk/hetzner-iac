// Command target resolves a component name into the URNs `pulumi --target`
// takes.
//
// It exists because of one measured thing: a `--target` that matches nothing
// SUCCEEDS. Run against this repository's own dev stack,
//
//	pulumi preview --target '**::Release::does-not-exist'
//	Resources:
//	    + 1 to create
//	    24 unchanged
//
// and an exit code of zero. So `task platform:apply target=cert-manger` would
// report success and apply nothing — the same shape of failure the taskfile's
// own comment records about a `for` loop over an empty list: "would have
// reported success and applied no layer at all".
//
// Pulumi will not catch that, so this does. A selector either resolves to URNs
// that exist in the stack's state, or this refuses and prints what the stack
// actually holds.
//
// The state, not a list kept beside it. There is no drift to guard against
// here by construction: the question "does this name exist" is asked of the
// only thing that knows.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// GroupPrefix selects a whole group rather than one resource.
//
// A group is a component resource, and its TYPE appears in the URN of every
// resource under it:
//
//	urn:…::hetzner-iac:platform:Ingress$kubernetes:helm.sh/v3:Release::traefik
//
// so selecting one is reading what the state already says rather than
// inferring anything. `group:Ingress` takes that resource, its siblings, and
// the group's own node.
const GroupPrefix = "group:"

// Resource is one entry of `pulumi stack --show-urns --output json`.
//
// Three fields is all that listing gives, and all this needs. `stack export`
// would give the whole checkpoint including every resource's inputs and the
// ciphertext of the Hetzner token — more than the question asks for, and not
// something to write to a pipe.
type Resource struct {
	URN  string `json:"urn"`
	Type string `json:"type"`
	Name string `json:"name"`
}

// listing is the shape of that command's output.
type listing struct {
	Resources []Resource `json:"resources"`
}

// ErrNoSelector is returned when the caller passed none. Distinct from "the
// selector matched nothing": an empty selector is a bug in the task, and a
// selector that matches nothing is a typo by an operator.
var ErrNoSelector = errors.New("no selector given")

// LayerName is what may be joined onto the layers/ path.
//
// A layer name rather than a path, and that is the contract change gosec asked
// for: a caller-supplied DIRECTORY reaches filepath.Join and exec, and no
// comment makes that safe. A name matched against this pattern and joined onto
// a root this program finds itself cannot leave the tree.
//
// The same shape internal/ci/layers_test.go matches layer references by, so a
// name this accepts is a name that repository's own gate would recognise.
var LayerName = regexp.MustCompile(`^[0-9]{2}-[a-z][a-z0-9-]*$`)

// LayerNameShape describes it for the error, beside the pattern it describes.
const LayerNameShape = "two digits, a hyphen, then lower-case letters, digits and hyphens — as in 30-cluster-services"

// LayersDirectory is where they live, relative to the repository root.
const LayersDirectory = "layers"

// StackName is what may be handed to the Pulumi CLI as --stack.
//
// Validated rather than annotated. Two of the three arguments reach an
// exec.Command, and gosec's taint analysis is right to ask about the one that
// comes straight from an operator: `task platform:plan stack=<anything>`. A
// `#nosec` here would be a promise; this is a check, and tools/recoverykit
// already made the same choice for the same reason.
//
// Pulumi's own stack names allow letters, digits, hyphens, underscores and
// periods. A leading period is refused, which is what keeps `..` out.
var StackName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)

// StackNameExtra names the rest for the error, so the message and the pattern
// cannot drift apart in a reader's head.
const StackNameExtra = "hyphens, underscores and periods, and not a leading period"

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 3 {
		return errors.New("usage: target <layer> <stack> <selector>\n" +
			"  layer:    a directory under layers/, such as 30-cluster-services\n" +
			"  selector: a resource name, Type:name, or " + GroupPrefix + "Type")
	}

	layer, stack, selector := args[0], args[1], args[2]

	if strings.TrimSpace(selector) == "" {
		return ErrNoSelector
	}

	if !StackName.MatchString(stack) {
		return fmt.Errorf("%q is not a stack name: letters, digits, %s", stack, StackNameExtra)
	}

	directory, err := layerDirectory(ctx, layer)
	if err != nil {
		return err
	}

	resources, resourcesErr := resourcesOf(ctx, directory, stack)
	if resourcesErr != nil {
		return resourcesErr
	}

	urns, err := Match(resources, selector)
	if err != nil {
		return err
	}

	for _, urn := range urns {
		fmt.Fprintln(out, urn)
	}

	return nil
}

// Match turns a selector into URNs, or explains why it cannot.
//
// Three outcomes, and the middle one is the reason this is a function rather
// than a grep:
//
//   - one or more matches: their URNs, in state order.
//   - nothing: an error naming what the stack does hold. An operator who
//     mistyped needs the list, not a shrug — and Pulumi's own answer to this
//     case is silent success.
//   - one name, two types: an error naming both, and how to disambiguate.
//     Silently picking one would target a resource the operator did not mean.
func Match(resources []Resource, selector string) ([]string, error) {
	if group, found := strings.CutPrefix(selector, GroupPrefix); found {
		return matchGroup(resources, group)
	}

	wantType, wantName := "", selector
	if before, after, qualified := strings.Cut(selector, ":"); qualified {
		wantType, wantName = before, after
	}

	var (
		urns  []string
		types []string
	)

	for _, resource := range resources {
		if resource.Name != wantName {
			continue
		}

		leaf := TypeLeaf(resource.Type)

		if wantType != "" && leaf != wantType {
			continue
		}

		urns = append(urns, resource.URN)
		types = append(types, leaf)
	}

	switch {
	case len(urns) == 0:
		return nil, noMatch(resources, selector)
	case len(urns) > 1 && wantType == "":
		// Every qualified form, not a guess at the intended one. Suggesting
		// the alphabetically first type would be arbitrary advice with the
		// authority of a tool behind it.
		qualified := make([]string, 0, len(types))
		for _, leaf := range types {
			qualified = append(qualified, leaf+":"+wantName)
		}

		slices.Sort(qualified)

		return nil, fmt.Errorf("%q names %d resources: say which one, as %s",
			selector, len(urns), strings.Join(slices.Compact(qualified), " or "))
	}

	return urns, nil
}

// GroupPackage is the package every group's type token begins with.
//
// Required, so that a group selector cannot mean a provider's resource. A
// Kubernetes Ingress is `kubernetes:networking.k8s.io/v1:Ingress` and a group
// named Ingress is `hetzner-iac:platform:Ingress` — the same type LEAF, and
// matching on the leaf alone made `group:Ingress` take every Ingress object in
// the cluster. Targeting more than was asked for is the one failure a tool
// about targeting must not have.
const GroupPackage = "hetzner-iac"

// nested is the separator Pulumi puts between a parent's type and a child's.
const nested = "$"

// matchGroup takes a component resource and everything under it.
//
// Both halves matter. The children are what an apply is usually about; the
// node itself is what a destroy has to remove as well, or the group's own
// entry is orphaned in the state.
//
// The node is found first, and its OWN type token is then what children are
// matched by. That removes the guesswork: a child's URN contains its parent's
// full type followed by `$`, so there is nothing to infer and no leaf to
// collide on.
func matchGroup(resources []Resource, group string) ([]string, error) {
	node, found := groupNode(resources, group)
	if !found {
		return nil, noMatch(resources, GroupPrefix+group)
	}

	urns := []string{node.URN}

	for _, resource := range resources {
		if strings.Contains(resource.URN, node.Type+nested) {
			urns = append(urns, resource.URN)
		}
	}

	return urns, nil
}

// groupNode is the component resource a group selector names.
func groupNode(resources []Resource, group string) (Resource, bool) {
	for _, resource := range resources {
		if strings.HasPrefix(resource.Type, GroupPackage+":") && TypeLeaf(resource.Type) == group {
			return resource, true
		}
	}

	return Resource{}, false
}

// TypeLeaf is the last segment of a Pulumi type token: Release from
// kubernetes:helm.sh/v3:Release, Ingress from hetzner-iac:platform:Ingress.
//
// The leaf rather than the whole token, because the whole token is what a
// selector should not have to carry: `Release:traefik` is something a person
// can type and `kubernetes:helm.sh/v3:Release:traefik` is not.
func TypeLeaf(token string) string {
	if index := strings.LastIndex(token, ":"); index >= 0 {
		return token[index+1:]
	}

	return token
}

// noMatch is the error an operator reads, so it carries the list.
//
// Qualified as Type:name rather than bare names, for two reasons. It says what
// each thing IS — a Release is targetable, a StackReference is not something
// anybody meant — and it is itself an example of the syntax that disambiguates
// a repeated name. URNs would be exact and unreadable; what a mistyped
// selector needs is the spelling it was reaching for.
//
// Nothing is filtered out. A list that hides the stack's own node and its
// providers would be tidier and would also be a judgement about what an
// operator is allowed to look for.
func noMatch(resources []Resource, selector string) error {
	qualified := make([]string, 0, len(resources))
	for _, resource := range resources {
		qualified = append(qualified, TypeLeaf(resource.Type)+":"+resource.Name)
	}

	slices.Sort(qualified)

	return fmt.Errorf(
		"%q matches nothing in this stack, and a --target that matches nothing "+
			"is an apply that reports success and does nothing.\nThis stack holds: %s",
		selector, strings.Join(slices.Compact(qualified), ", "))
}

// ProjectFile is what makes a directory a Pulumi project.
const ProjectFile = "Pulumi.yaml"

// layerDirectory turns a layer name into the directory Pulumi is pointed at.
//
// Relative to the repository root, found the same way tools/golangci finds it,
// so this works from whatever directory the task happens to run in.
func layerDirectory(ctx context.Context, layer string) (string, error) {
	// Checked here rather than in run, so the pattern and the join it protects
	// are in one function — which is also what makes gosec's taint analysis
	// able to see that the name reaching filepath.Join was validated.
	if !LayerName.MatchString(layer) {
		return "", fmt.Errorf("%q is not a layer: %s", layer, LayerNameShape)
	}

	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("find the repository root: %w", err)
	}

	directory := filepath.Join(strings.TrimSpace(string(out)), LayersDirectory, layer)

	// Said here because `pulumi --cwd` on a directory with no Pulumi.yaml
	// fails with a message about the current project rather than the argument.
	//
	// #nosec G703 -- the only caller-supplied part of this path is `layer`,
	// matched against LayerName four lines above: two digits, a hyphen and
	// lower-case letters, which cannot express a separator or a `..`. gosec's
	// taint analysis does not recognise a regexp match as a sanitiser, so the
	// annotation stands on a check that is really there rather than instead of
	// one.
	if _, statErr := os.Stat(filepath.Join(directory, ProjectFile)); statErr != nil {
		return "", fmt.Errorf("%s is not a Pulumi project: %w", directory, statErr)
	}

	return directory, nil
}

// StackCommand is the listing this parses, and it is a contract rather than a
// convenience: `pulumi stack --show-urns` without --output prints a tree laid
// out for a terminal, and its columns move between releases.
var StackCommand = []string{"stack", "--show-urns", "--output", "json"}

// resourcesOf asks the stack what it holds.
func resourcesOf(ctx context.Context, directory, stack string) ([]Resource, error) {
	argv := append([]string{"--non-interactive", "--cwd", directory, "--stack", stack}, StackCommand...)

	// #nosec G204,G702 -- the binary is a literal; the stack name is checked
	// against StackName above and the directory against ProjectFile, so
	// neither reaches here unvalidated.
	out, err := exec.CommandContext(ctx, "pulumi", argv...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, fmt.Errorf("read the stack's resources: %w: %s",
				err, strings.TrimSpace(string(exit.Stderr)))
		}

		return nil, fmt.Errorf("read the stack's resources: %w", err)
	}

	return resourcesIn(out)
}

// resourcesIn parses that output, separated so a test can hold the parser to
// the shape the CLI produces without running it.
func resourcesIn(raw []byte) ([]Resource, error) {
	var parsed listing

	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse the stack listing: %w", err)
	}

	if len(parsed.Resources) == 0 {
		return nil, errors.New("the stack holds no resources: apply it before targeting part of it")
	}

	return parsed.Resources, nil
}
