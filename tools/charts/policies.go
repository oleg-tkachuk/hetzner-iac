package main

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
)

// policyManifests is where 20-network-policy keeps its policies, from the
// repository root.
const policyManifests = "layers/20-network-policy/manifests"

// The label keys a Cilium selector writes, as Cilium spells them: a pod's own
// labels carry the k8s: source prefix, and the namespace is a label Cilium
// derives.
const (
	ciliumLabelPrefix = "k8s:"
	namespaceLabel    = ciliumLabelPrefix + "io.kubernetes.pod.namespace"
)

// selectorIn is the one matchExpressions operator the policies use.
const selectorIn = "In"

// notFromAChart are the selectors whose pods no chart here renders, each with
// the reason. Anything else that selects nothing a chart renders is a policy
// that quietly applies to no pod.
var notFromAChart = map[string]string{
	"k8s:k8s-app=kube-dns":                        "CoreDNS is Talos's, not a chart's",
	"k8s:acme.cert-manager.io/http01-solver=true": "cert-manager creates the solver pod per order",
	"k8s:app.kubernetes.io/name=prometheus":       "observability is deployed by the cluster's users",
}

// ciliumPolicy is the part of a CiliumClusterwideNetworkPolicy this reads.
type ciliumPolicy struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		EndpointSelector metav1.LabelSelector `json:"endpointSelector"`
		Ingress          []ciliumRule         `json:"ingress"`
		Egress           []ciliumRule         `json:"egress"`
	} `json:"spec"`
}

type ciliumRule struct {
	FromEndpoints []metav1.LabelSelector `json:"fromEndpoints"`
	ToEndpoints   []metav1.LabelSelector `json:"toEndpoints"`
}

// renderedPod is one pod a chart renders: its namespace and labels.
type renderedPod struct {
	namespace string
	labels    map[string]string
}

// checkPolicySelectors holds every network-policy selector that names a pod to
// a pod some chart renders.
//
// A selector that matches nothing is accepted by Kubernetes and by Cilium and
// does nothing: a chart that renames a label leaves its pods outside the policy
// that allowed their traffic, and under the default deny that traffic is
// dropped while every check reports the policy valid.
func checkPolicySelectors(ctx context.Context) int {
	pods, err := renderedPods(ctx)
	if err != nil {
		fmt.Printf("MISS  %-14s %v\n", "policies", err)

		return 1
	}

	selectors, err := policySelectors(policyManifests)
	if err != nil {
		fmt.Printf("MISS  %-14s %v\n", "policies", err)

		return 1
	}

	problems := unmatchedSelectors(selectors, pods)
	for _, problem := range problems {
		fmt.Printf("MISS  %-14s %s\n", "policies", problem)
	}

	if len(problems) == 0 {
		fmt.Printf("ok    %-14s every policy selector names a pod a chart renders\n", "policies")
	}

	return len(problems)
}

// namedSelector is a selector and where it was written.
type namedSelector struct {
	where    string
	selector metav1.LabelSelector
}

// policySelectors reads every endpoint selector in the policy files.
func policySelectors(dir string) ([]namedSelector, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}

	var out []namedSelector

	for _, file := range files {
		raw, err := os.ReadFile(file) // #nosec G304 -- files globbed from a constant directory
		if err != nil {
			return nil, err
		}

		docs, err := documents(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}

		for _, doc := range docs {
			var policy ciliumPolicy
			if err := yaml.Unmarshal([]byte(doc.Text), &policy); err != nil {
				return nil, fmt.Errorf("%s: %w", file, err)
			}

			where := filepath.Base(file) + " " + policy.Metadata.Name
			out = append(out, namedSelector{where, policy.Spec.EndpointSelector})

			for _, rule := range slices.Concat(policy.Spec.Ingress, policy.Spec.Egress) {
				for _, s := range slices.Concat(rule.FromEndpoints, rule.ToEndpoints) {
					out = append(out, namedSelector{where, s})
				}
			}
		}
	}

	return out, nil
}

// unmatchedSelectors is one message per selector that names a pod no chart
// renders. A selector naming no pod label — empty, or a namespace alone —
// names no pod and is not held here.
func unmatchedSelectors(selectors []namedSelector, pods []renderedPod) []string {
	var problems []string

	for _, s := range selectors {
		podKeys := podTerms(s.selector)
		if len(podKeys) == 0 {
			continue
		}

		if externalSelector(podKeys) {
			continue
		}

		// Each alternative of an In list on its own: a list of two pods that
		// still matches one would hide the other having gone.
		for _, alternative := range alternatives(s.selector) {
			if !slices.ContainsFunc(pods, func(p renderedPod) bool { return matches(alternative, p) }) {
				problems = append(problems, fmt.Sprintf("%s selects %s, and no chart renders such a pod",
					s.where, strings.Join(podTerms(alternative), ", ")))
			}
		}
	}

	sort.Strings(problems)

	return slices.Compact(problems)
}

// podTerms are a selector's pod-label requirements, written key=value, without
// the namespace.
func podTerms(s metav1.LabelSelector) []string {
	var terms []string

	for key, value := range s.MatchLabels {
		if key != namespaceLabel {
			terms = append(terms, key+"="+value)
		}
	}

	for _, e := range s.MatchExpressions {
		if e.Key != namespaceLabel {
			terms = append(terms, e.Key+" in "+strings.Join(e.Values, "|"))
		}
	}

	sort.Strings(terms)

	return terms
}

// externalSelector reports whether a selector names a pod notFromAChart lists.
func externalSelector(terms []string) bool {
	return slices.ContainsFunc(terms, func(term string) bool {
		_, found := notFromAChart[term]

		return found
	})
}

// alternatives splits a selector's pod-label In expressions into one selector
// per value, so each named pod is looked for separately. The namespace
// expression stays as it is.
func alternatives(s metav1.LabelSelector) []metav1.LabelSelector {
	out := []metav1.LabelSelector{{MatchLabels: maps.Clone(s.MatchLabels)}}

	for _, e := range s.MatchExpressions {
		if e.Key == namespaceLabel || string(e.Operator) != selectorIn {
			for i := range out {
				out[i].MatchExpressions = append(out[i].MatchExpressions, e)
			}

			continue
		}

		var split []metav1.LabelSelector

		for _, base := range out {
			for _, value := range e.Values {
				next := metav1.LabelSelector{MatchLabels: maps.Clone(base.MatchLabels), MatchExpressions: slices.Clone(base.MatchExpressions)}
				if next.MatchLabels == nil {
					next.MatchLabels = map[string]string{}
				}

				next.MatchLabels[e.Key] = value
				split = append(split, next)
			}
		}

		out = split
	}

	return out
}

// matches is a Cilium endpoint selector evaluated against a rendered pod.
func matches(s metav1.LabelSelector, pod renderedPod) bool {
	value := func(key string) (string, bool) {
		if key == namespaceLabel {
			return pod.namespace, true
		}

		v, found := pod.labels[strings.TrimPrefix(key, ciliumLabelPrefix)]

		return v, found
	}

	for key, want := range s.MatchLabels {
		if got, found := value(key); !found || got != want {
			return false
		}
	}

	for _, e := range s.MatchExpressions {
		got, found := value(e.Key)
		if string(e.Operator) != selectorIn || !found || !slices.Contains(e.Values, got) {
			return false
		}
	}

	return true
}

// renderedPods is every pod template every chart renders with its values.
func renderedPods(ctx context.Context) ([]renderedPod, error) {
	var pods []renderedPod

	for _, key := range charts.Keys() {
		chart, err := charts.Get(key)
		if err != nil {
			return nil, err
		}

		output, err := renderRaw(ctx, chart, key, chart.Namespace, key)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}

		found, err := podTemplates(output, chart.Namespace)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}

		pods = append(pods, found...)
	}

	return pods, nil
}

// podTemplates is each workload's pod namespace and labels in a render.
func podTemplates(manifests []byte, releaseNamespace string) ([]renderedPod, error) {
	docs, err := documents(manifests)
	if err != nil {
		return nil, err
	}

	var pods []renderedPod

	for _, doc := range docs {
		var template *corev1.PodTemplateSpec

		switch doc.Kind {
		case string(charts.Deployment):
			var o appsv1.Deployment
			if err := yaml.Unmarshal([]byte(doc.Text), &o); err != nil {
				return nil, err
			}

			template = &o.Spec.Template
		case string(charts.StatefulSet):
			var o appsv1.StatefulSet
			if err := yaml.Unmarshal([]byte(doc.Text), &o); err != nil {
				return nil, err
			}

			template = &o.Spec.Template
		case string(charts.DaemonSet):
			var o appsv1.DaemonSet
			if err := yaml.Unmarshal([]byte(doc.Text), &o); err != nil {
				return nil, err
			}

			template = &o.Spec.Template
		default:
			continue
		}

		pods = append(pods, renderedPod{namespace: doc.NamespaceOr(releaseNamespace), labels: template.Labels})
	}

	return pods, nil
}
