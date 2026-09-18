// Package talossecrets reads a cluster's Talos secrets bundle out of the
// Pulumi state that holds it.
//
// The bundle is the cluster's root of trust: the CA keys every node and client
// certificate descends from. Nothing regenerates it, so a cluster whose state
// is lost cannot be joined, upgraded or restored — which is why two commands
// exist to get it out, `tools/secrets` and `tools/recoverykit`.
//
// Separate from internal/pkg/hcloudtoken, which also reads a stack, because
// this one reads it by running the `pulumi` binary and that one needs the
// automation API. Together they would put 800 packages behind a function that
// shells out.
package talossecrets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

// SecretsResourceType is the Talos secrets bundle in Pulumi's state — the
// cluster's root of trust. NewCluster creates exactly one, under Protect.
//
// #nosec G101 -- a provider's resource type token, which appears in every
// state file Pulumi writes. The word "secrets" in it is what the taint
// analysis matches on; there is no credential here to leak.
const SecretsResourceType = "talos:machine/secrets:Secrets"

// Pulumi wraps a secret value in an envelope rather than storing it bare:
// an object carrying the signature below and the value under `plaintext`,
// itself JSON-encoded. `--show-secrets` decrypts the value and leaves the
// envelope.
//
// Named because two places must spell them identically — and because the
// signature is how a secret is told apart from Pulumi's other envelopes, an
// asset or an output value, which must pass through untouched.
// #nosec G101 -- Pulumi's own public signature constants, documented and
// identical in every state file ever written. Flagged for looking like hex,
// which is exactly what a signature looks like.
const (
	// gitleaks:allow -- published constants, not credentials. gitleaks scans
	// the staged diff, where a line has no context and hex reads as entropy.
	secretSignatureKey = "4dabf18193072939515e22adb298388d" // gitleaks:allow
	secretSignature    = "1b47061264138c4ac30d75fd1eb44270" // gitleaks:allow
	plaintextKey       = "plaintext"
)

// enginePrefix marks the state keys Pulumi keeps for its own bookkeeping
// (`__meta`, `__pulumi_raw_state_delta`). They describe the state, not the
// cluster, and carrying them into a stored copy invites the reader to think
// they matter.
const enginePrefix = "__"

// Bundle returns the cluster's Talos secrets — the CA keys every node and
// client certificate descends from, the bootstrap tokens, and the secretbox
// key that encrypts Kubernetes Secrets inside etcd.
//
// It is read out of state rather than published as a stack output, and that
// is the whole design. Every layer reads this stack through a
// StackReference, so an output travels to all of them; the Hetzner token
// travels that way on purpose because the CCM and the CSI driver need it,
// but nothing above the cluster tier has any use for the CA, and it is
// strictly more powerful.
//
// `pulumi stack export --show-secrets` rather than the automation API:
// Workspace.ExportStack has no equivalent of that flag, so it returns the
// bundle as the ciphertext it is stored as — which is a copy that only the
// backend holding the key can read, and the backend is exactly what a second
// copy exists to survive.
func Bundle(ctx context.Context, stack string) ([]byte, error) {
	if stack == "" {
		return nil, fmt.Errorf("no stack to read the secrets bundle from")
	}

	// #nosec G204 -- the arguments are literals from this file plus a stack
	// name, passed as a vector: there is no shell to interpret any of it.
	cmd := exec.CommandContext(ctx, "pulumi", "--non-interactive",
		"--cwd", clusterspec.ClusterDir, "--stack", stack, "stack", "export", "--show-secrets")

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return nil, fmt.Errorf("pulumi stack export: %w", err)
		}

		return nil, fmt.Errorf("pulumi stack export: %w\n%s", err, message)
	}

	return bundleFrom(out, stack)
}

// exportedStack is the part of `pulumi stack export` this reads.
type exportedStack struct {
	Deployment struct {
		Resources []struct {
			URN     string                     `json:"urn"`
			Type    string                     `json:"type"`
			Outputs map[string]json.RawMessage `json:"outputs"`
		} `json:"resources"`
	} `json:"deployment"`
}

// bundleFrom picks the secrets resource out of an exported stack, separated
// from the call so it can be tested without a backend.
//
// Exactly one, or an error. Zero means the stack has never been applied, or
// is not the cluster tier at all — and an empty file stored as a backup is
// worse than no file, because it is only read on the day it is needed. More
// than one means an assumption this repository is built on has stopped
// holding, and guessing which is the cluster's own is not a thing to do with
// a certificate authority.
func bundleFrom(raw []byte, stack string) ([]byte, error) {
	var exported exportedStack
	if err := json.Unmarshal(raw, &exported); err != nil {
		return nil, fmt.Errorf("pulumi stack export returned no usable json: %w", err)
	}

	var found []map[string]json.RawMessage

	for _, resource := range exported.Deployment.Resources {
		if resource.Type != SecretsResourceType {
			continue
		}

		bundle := map[string]json.RawMessage{}

		for key, value := range resource.Outputs {
			if strings.HasPrefix(key, enginePrefix) {
				continue
			}

			bundle[key] = value
		}

		found = append(found, bundle)
	}

	switch len(found) {
	case 1:
		unwrapped, err := unwrapSecrets(found[0])
		if err != nil {
			return nil, err
		}

		// Indented, because this is stored and later read by a person under
		// pressure.
		return json.MarshalIndent(unwrapped, "", "  ")

	case 0:
		return nil, fmt.Errorf(
			"stack %s holds no %s: apply the cluster tier first, or name the stack that has one",
			stack, SecretsResourceType)

	default:
		return nil, fmt.Errorf(
			"stack %s holds %d resources of type %s, and choosing between certificate authorities is not a guess worth making",
			stack, len(found), SecretsResourceType)
	}
}

// unwrapSecrets replaces every Pulumi secret envelope with the value inside
// it, so what is stored is the bundle Talos describes rather than the shape
// Pulumi stores it in.
//
// Without this the file holds `{"4dabf…": "1b47…", "plaintext": "\"abc\""}`
// wherever a key or a token belongs, and unwrapping it falls to whoever is
// restoring a cluster — which is the worst moment to ask anyone to learn
// Pulumi's state format.
//
// An envelope with no plaintext is an error rather than a dropped key: a
// bundle missing one certificate authority restores a cluster that rejects
// every certificate in it, and says nothing until it does.
func unwrapSecrets(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]json.RawMessage:
		out := make(map[string]any, len(typed))

		for key, raw := range typed {
			var decoded any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				return nil, fmt.Errorf("read %s from the exported stack: %w", key, err)
			}

			unwrapped, err := unwrapSecrets(decoded)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}

			out[key] = unwrapped
		}

		return out, nil

	case map[string]any:
		if signature, marked := typed[secretSignatureKey]; marked && signature == secretSignature {
			encoded, present := typed[plaintextKey]
			if !present {
				return nil, fmt.Errorf("a secret in the exported stack carries no %s: "+
					"export it again, and with --show-secrets", plaintextKey)
			}

			text, isString := encoded.(string)
			if !isString {
				return nil, fmt.Errorf("a secret's %s is %T rather than a string", plaintextKey, encoded)
			}

			var plain any
			if err := json.Unmarshal([]byte(text), &plain); err != nil {
				return nil, fmt.Errorf("a secret's %s is not json: %w", plaintextKey, err)
			}

			return plain, nil
		}

		out := make(map[string]any, len(typed))

		for key, nested := range typed {
			unwrapped, err := unwrapSecrets(nested)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}

			out[key] = unwrapped
		}

		return out, nil

	case []any:
		out := make([]any, len(typed))

		for i, nested := range typed {
			unwrapped, err := unwrapSecrets(nested)
			if err != nil {
				return nil, err
			}

			out[i] = unwrapped
		}

		return out, nil

	default:
		return value, nil
	}
}
