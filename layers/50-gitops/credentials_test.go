package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTransportOf_ReadsTheFormGitActuallyUses covers both spellings and the
// one that looks like the other.
//
// Every fixture names a reserved domain rather than a real forge: RFC 2606
// keeps example.com for exactly this, TestTrackedFiles_NameNoDomainOfTheirOwn
// holds the repository to it, and a fixture naming a real host reads as
// something that was tested against it.
//
// `https://user@host/repo` carries an `@` and is not SSH, which is why the
// scheme is read first and only a URL without `://` is treated as scp-like. A
// transport read wrongly rejects a credential that is correct.
func TestTransportOf_ReadsTheFormGitActuallyUses(t *testing.T) {
	t.Parallel()

	for url, want := range map[string]Transport{
		"https://example.com/owner/repo.git":        TransportHTTPS,
		"http://git.example.com/owner/repo.git":     TransportHTTPS,
		"https://token@example.com/owner/repo.git":  TransportHTTPS,
		"ssh://git@example.com/owner/repo.git":      TransportSSH,
		"ssh://git@git.example.com:2222/owner/repo": TransportSSH,
		// What every forge prints in its clone box.
		"git@example.com:owner/repo.git": TransportSSH,
		// Neither, and Argo CD would reject each of them.
		"example.com/owner/repo": TransportUnknown,
		"file:///srv/git/repo":   TransportUnknown,
		"git@example.com":        TransportUnknown,
		"":                       TransportUnknown,
	} {
		assert.Equal(t, want, TransportOf(url), "%q", url)
	}
}

// fakeKey stands in for an SSH private key, and is deliberately not shaped
// like one.
//
// A PEM header here was the obvious fixture and gitleaks refused the commit
// for it — correctly: a scanner that learns to ignore `BEGIN OPENSSH PRIVATE
// KEY` in a test file is a scanner that will ignore the real one. Nothing in
// these tests reads the value, only whether it is empty.
const fakeKey = "an-ssh-key"

// TestValidate_RefusesWhatArgoCDWouldAcceptAndFailOn is the whole point of
// this validation.
//
// None of these fails the apply without it. The Secret is created, Argo CD
// reads it, and the repository shows an authentication error in a UI while
// `pulumi up` reported success — with the wrong value in stack config and
// nothing in that error naming it.
func TestValidate_RefusesWhatArgoCDWouldAcceptAndFailOn(t *testing.T) {
	t.Parallel()

	const (
		https = "https://example.com/owner/repo.git"
		ssh   = "git@example.com:owner/repo.git"
	)

	for name, tc := range map[string]struct {
		repoURL    string
		credential RepoCredential
		says       string
	}{
		"both forms at once": {
			repoURL:    https,
			credential: RepoCredential{Username: "git", Password: "t", SSHKey: "KEY"},
			says:       "both an SSH key and a username/password",
		},
		"username without password": {
			repoURL:    https,
			credential: RepoCredential{Username: "git"},
			says:       "HTTPS needs both halves",
		},
		"password without username": {
			repoURL:    https,
			credential: RepoCredential{Password: "token"},
			says:       "HTTPS needs both halves",
		},
		"ssh url with a password": {
			repoURL:    ssh,
			credential: RepoCredential{Username: "git", Password: "token"},
			says:       "is an SSH URL and the credential is a username/password",
		},
		"https url with a key": {
			repoURL:    https,
			credential: RepoCredential{SSHKey: "KEY"},
			says:       "is an HTTPS URL and the credential is an SSH key",
		},
		"a url in neither form": {
			repoURL:    "example.com/owner/repo",
			credential: RepoCredential{SSHKey: "KEY"},
			says:       "neither an https:// nor an ssh:// URL",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := tc.credential.Validate(tc.repoURL)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.says)
		})
	}
}

// TestValidate_AcceptsTheTwoFormsThatWork is the other half: a validation that
// refuses a working credential is worse than none, because the remedy is to
// delete the check.
func TestValidate_AcceptsTheTwoFormsThatWork(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		repoURL    string
		credential RepoCredential
	}{
		"https with a token": {
			repoURL:    "https://example.com/owner/repo.git",
			credential: RepoCredential{Username: "git", Password: "ghp_x"},
		},
		"scp-like ssh with a deploy key": {
			repoURL:    "git@example.com:owner/repo.git",
			credential: RepoCredential{SSHKey: fakeKey},
		},
		"ssh scheme with a deploy key": {
			repoURL:    "ssh://git@example.com/owner/repo.git",
			credential: RepoCredential{SSHKey: fakeKey},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.NoError(t, tc.credential.Validate(tc.repoURL))
		})
	}
}

// TestValidate_SaysNothingAboutAPublicRepository keeps the common case free of
// configuration.
//
// A public repository needs no credential, and the URL is not even checked
// then: an unset credential cannot be wrong about a transport.
func TestValidate_SaysNothingAboutAPublicRepository(t *testing.T) {
	t.Parallel()

	var none RepoCredential

	assert.False(t, none.Configured())
	assert.NoError(t, none.Validate("example.com/owner/repo"))
}

// TestRepositorySecret_IsRecognisedByLabel pins the strings Argo CD looks for.
//
// The label is the entire mechanism: a Secret with every field right and this
// label misspelt is ignored, and the repository reports as unauthenticated
// with nothing pointing at the Secret sitting beside it in the namespace. The
// field names are Argo CD's too, not names chosen here.
func TestRepositorySecret_IsRecognisedByLabel(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "argocd.argoproj.io/secret-type", SecretTypeLabel)
	assert.Equal(t, "repository", SecretTypeRepository)

	assert.Equal(t, "url", FieldURL)
	assert.Equal(t, "username", FieldUsername)
	assert.Equal(t, "password", FieldPassword)
	assert.Equal(t, "sshPrivateKey", FieldSSHPrivateKey)
}
