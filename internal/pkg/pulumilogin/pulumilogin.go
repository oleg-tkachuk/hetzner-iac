// Package pulumilogin refuses to run against a Pulumi backend this machine
// holds no credential for.
package pulumilogin

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"

	"github.com/pulumi/pulumi/sdk/v3/go/common/env"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
)

// ProjectFile is the Pulumi project file each project directory holds.
const ProjectFile = "Pulumi.yaml"

// loginSchemes are the backends a login stores a credential for: Pulumi
// Cloud and its self-hosted form. A DIY backend — file://, s3://, gs:// —
// needs no account, so there is nothing to check.
func loginSchemes() map[string]bool {
	return map[string]bool{"https": true, "http": true}
}

// ErrNotLoggedIn is returned when a backend has no credential.
var ErrNotLoggedIn = errors.New("not logged in")

// Require refuses to run against a backend this machine holds no
// credential for, before the first CLI call.
//
// Run by an AI coding agent, the Pulumi CLI does not fail when it is not
// logged in: it signs up a temporary agent account and answers from that
// account's empty backend, so the report says every stack is missing. The
// check reads the credentials store the CLI itself reads, through the SDK, so
// the CLI never gets the chance.
func Require(dirs []string) error {
	if env.AccessToken.Value() != "" {
		return nil
	}

	checked := map[string]bool{}

	for _, dir := range dirs {
		project, err := workspace.LoadProject(filepath.Join(dir, ProjectFile))
		if err != nil {
			return fmt.Errorf("read %s: %w", filepath.Join(dir, ProjectFile), err)
		}

		if project.Backend == nil || checked[project.Backend.URL] || !needsLogin(project.Backend.URL) {
			continue
		}

		checked[project.Backend.URL] = true

		if err := loggedIn(project.Backend.URL); err != nil {
			return err
		}
	}

	return nil
}

// needsLogin reports whether a backend URL is one a login holds a credential for.
func needsLogin(backend string) bool {
	parsed, err := url.Parse(backend)

	return err == nil && loginSchemes()[parsed.Scheme]
}

func loggedIn(backend string) error {
	account, err := workspace.GetAccount(backend)
	if err != nil {
		return fmt.Errorf("read the Pulumi credentials for %s: %w", backend, err)
	}

	if !account.HasCredential() {
		return fmt.Errorf("%w to %s. Log in first:\n\n  pulumi login %s\n\n"+
			"or export %s", ErrNotLoggedIn, backend, backend, env.AccessToken.Var().Name())
	}

	return nil
}
