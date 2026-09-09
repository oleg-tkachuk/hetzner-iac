//go:build e2e

// Package e2e verifies a real cluster, not a resource graph.
//
// The unit tests elsewhere pin what Pulumi will ASK for. These check what
// actually happened — which is where the interesting failures live: a Cilium
// configuration that is right in the values map and wrong against this Talos
// version, a CCM that starts and manages no routes, an ingress Service that
// waits forever for a load balancer.
//
// Behind a build tag, so `go test ./...` never tries to reach a cluster. Run
// with:
//
//	task e2e stack=prod
//
// The suite is READ-ONLY apart from one namespace it creates and deletes. It
// is safe against production, which is the point: the cluster worth verifying
// is the one that is running.
package e2e

import (
	"log"
	"os"
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

var testenv env.Environment

func TestMain(m *testing.M) {
	// Against an EXISTING cluster: this suite verifies the cluster this
	// repository built, so spinning up a throwaway kind cluster would test
	// something else entirely — none of the Hetzner integration exists there.
	cfg, err := envconf.NewFromFlags()
	if err != nil {
		log.Fatalf("e2e: build config from flags: %v", err)
	}

	if cfg.KubeconfigFile() == "" {
		log.Fatal("e2e: no kubeconfig — run `task cluster:kubeconfig` and export KUBECONFIG")
	}

	testenv = env.NewWithConfig(cfg)

	os.Exit(testenv.Run(m))
}
