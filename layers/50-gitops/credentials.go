package main

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"

	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Config keys for the credential a private root repository needs. Each is a
// secret in the stack config, the same way the Hetzner token is.
const (
	RepoUsernameKey      = "repoUsername"
	RepoPasswordKey      = "repoPassword"
	RepoSSHPrivateKeyKey = "repoSSHPrivateKey" // #nosec G101 -- a config key's name, not its value
)

// RepositoryCredential is the component name the root Application follows.
const RepositoryCredential = "repository-credential"

// RepositorySecret is what the Secret is called in the cluster. Argo CD finds
// it by LABEL rather than by name, so the name is only what an operator sees
// in `kubectl -n argocd get secrets`.
const RepositorySecret = "root-repository" // #nosec G101 -- a Secret's name, not a credential

// How Argo CD recognises the Secret at all.
//
// The label is the whole mechanism: a Secret with the right fields and no
// label is ignored, and Argo CD reports the repository as unauthenticated with
// nothing pointing at the Secret sitting beside it.
//
// `repository` covers one repository, named by the `url` field. The other form
// Argo CD accepts, `repo-creds`, matches every repository whose URL starts
// with its own — the shape to reach for once there are several private ones,
// and deliberately not what this writes: a prefix is a guess about
// repositories this layer has not been told about.
const (
	SecretTypeLabel      = "argocd.argoproj.io/secret-type" // #nosec G101 -- a label key, not a credential
	SecretTypeRepository = "repository"
)

// Fields of the Secret, which are Argo CD's names and not ours.
const (
	FieldURL           = "url"
	FieldUsername      = "username"
	FieldPassword      = "password"
	FieldSSHPrivateKey = "sshPrivateKey"
)

// Transport is how Argo CD will reach a repository, which its URL decides.
type Transport int

const (
	// TransportUnknown is a URL in neither form. Argo CD would reject it, so
	// this is reported here instead — at apply, beside the value that is
	// wrong.
	TransportUnknown Transport = iota
	TransportHTTPS
	TransportSSH
)

// TransportOf reads the transport out of a repository URL.
//
// Two forms, because git has two: a scheme, and the scp-like `git@host:path`
// that every forge prints in its clone box. The `@` alone does not decide it —
// `https://user@host/repo` carries one too — so a URL with `://` is read by
// its scheme and only the rest is treated as scp-like.
func TransportOf(repoURL string) Transport {
	if scheme, _, found := strings.Cut(repoURL, "://"); found {
		switch scheme {
		case "ssh":
			return TransportSSH
		case "https", "http":
			return TransportHTTPS
		default:
			return TransportUnknown
		}
	}

	// scp-like: a host, a colon, and a path. The colon has to come after an
	// `@`, or this is not a location at all.
	if user, rest, found := strings.Cut(repoURL, "@"); found && user != "" && strings.Contains(rest, ":") {
		return TransportSSH
	}

	return TransportUnknown
}

// RepoCredential is how Argo CD authenticates to the root repository, and
// nothing about which repository that is.
type RepoCredential struct {
	Username string
	Password string
	SSHKey   string
}

// Configured reports whether any half of any form was given.
func (c RepoCredential) Configured() bool {
	return c.Username != "" || c.Password != "" || c.SSHKey != ""
}

// Validate refuses the combinations Argo CD would accept and then fail on.
//
// Every one of these fails the same way without this: the Secret is created,
// Argo CD reads it, and the repository shows an authentication error in the UI
// while `pulumi up` reported success. The value that is wrong is in stack
// config, and nothing in that error names it.
func (c RepoCredential) Validate(repoURL string) error {
	if !c.Configured() {
		return nil
	}

	https := c.Username != "" || c.Password != ""

	if https && c.SSHKey != "" {
		return fmt.Errorf("both an SSH key and a username/password are set for %s: "+
			"unset %s, or unset %s and %s", repoURL, RepoSSHPrivateKeyKey, RepoUsernameKey, RepoPasswordKey)
	}

	if https && (c.Username == "" || c.Password == "") {
		return fmt.Errorf("HTTPS needs both halves: %s and %s, and one of them is unset. "+
			"For a forge token the username is any non-empty string and the token is the password",
			RepoUsernameKey, RepoPasswordKey)
	}

	switch TransportOf(repoURL) {
	case TransportSSH:
		if https {
			return fmt.Errorf("%s is an SSH URL and the credential is a username/password: "+
				"set %s instead, or point %s at the https:// form of the same repository",
				repoURL, RepoSSHPrivateKeyKey, RepoURLKey)
		}
	case TransportHTTPS:
		if c.SSHKey != "" {
			return fmt.Errorf("%s is an HTTPS URL and the credential is an SSH key: "+
				"set %s and %s instead, or point %s at the ssh:// form of the same repository",
				repoURL, RepoUsernameKey, RepoPasswordKey, RepoURLKey)
		}
	case TransportUnknown:
		return fmt.Errorf("%s is neither an https:// nor an ssh:// URL, and not the "+
			"git@host:path form either, so Argo CD cannot tell how to reach it", repoURL)
	}

	return nil
}

// repoCredential reads the credential out of stack config.
//
// Presence through Get and the value through RequireSecret, the same split
// 10-node-platform uses for the Hetzner token: Get answers "was this set" on a
// plain string, and RequireSecret returns an Output that stays marked as a
// secret through the apply and into state.
func repoCredential(r *layer.Runner) (RepoCredential, map[string]pulumi.StringOutput) {
	credential := RepoCredential{
		Username: r.Cfg.Get(RepoUsernameKey),
		Password: r.Cfg.Get(RepoPasswordKey),
		SSHKey:   r.Cfg.Get(RepoSSHPrivateKeyKey),
	}

	values := map[string]pulumi.StringOutput{}

	if credential.SSHKey != "" {
		values[FieldSSHPrivateKey] = r.Cfg.RequireSecret(RepoSSHPrivateKeyKey)
	}

	if credential.Username != "" {
		values[FieldUsername] = r.Cfg.RequireSecret(RepoUsernameKey)
	}

	if credential.Password != "" {
		values[FieldPassword] = r.Cfg.RequireSecret(RepoPasswordKey)
	}

	return credential, values
}

// createRepoCredential makes the Secret Argo CD authenticates with, or none
// and says what that means.
//
// Declining rather than failing when nothing is configured, because a public
// repository needs no credential and that is the case this repository itself
// documents. What it must not do is stay quiet: a private repository with no
// credential leaves the root Application reporting an authentication error
// that names no cause, minutes after an apply that said it succeeded.
func createRepoCredential(r *layer.Runner, dependencies []pulumi.Resource) (pulumi.Resource, error) {
	repoURL := r.Cfg.Get(RepoURLKey)

	credential, fields := repoCredential(r)

	if repoURL == "" {
		if credential.Configured() {
			// Config that cannot take effect, which is the silent kind. The
			// Secret would be created, labelled, and matched against no
			// repository at all.
			return nil, fmt.Errorf("a repository credential is set and %s is not, "+
				"so it would authenticate to nothing. Set %s, or unset the credential",
				RepoURLKey, RepoURLKey)
		}

		return nil, nil
	}

	if err := credential.Validate(repoURL); err != nil {
		return nil, err
	}

	if !credential.Configured() {
		r.Log.Skipped(RepositoryCredential,
			"none set — fine for a public repository, and an authentication error for a private one")

		return nil, nil
	}

	form := FieldSSHPrivateKey
	if credential.SSHKey == "" {
		form = FieldUsername + "/" + FieldPassword
	}

	r.Log.Step(RepositoryCredential, form)

	data := pulumi.StringMap{FieldURL: pulumi.String(base64.StdEncoding.EncodeToString([]byte(repoURL)))}

	for field, value := range fields {
		data[field] = layer.Base64Of(value)
	}

	return corev1.NewSecret(r.Ctx, RepositorySecret, &corev1.SecretArgs{
		Metadata: &metav1.ObjectMetaArgs{
			Name:      pulumi.String(RepositorySecret),
			Namespace: pulumi.String(Namespace),
			Labels:    pulumi.StringMap{SecretTypeLabel: pulumi.String(SecretTypeRepository)},
		},
		// Data, base64, not StringData — the same reason 10-node-platform
		// gives: stringData is write-only, Kubernetes folds it into data, and
		// the provider records data in state. A program setting stringData
		// diffs against its own last apply and plans to replace the Secret on
		// every run, for ever.
		Data: data,
	}, r.With(layer.DependsOn(dependencies)...)...)
}
