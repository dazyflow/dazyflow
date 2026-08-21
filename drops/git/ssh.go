// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package git

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	gogittransport "github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/skeema/knownhosts"
	gossh "golang.org/x/crypto/ssh"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
)

// GitCred is the material for one named, per-org git credential. It may carry
// an SSH key (for git@/ssh:// URLs), an HTTPS access token / PAT (for
// https:// URLs), or both — git_checkout uses whichever the repo URL needs.
type GitCred struct {
	PrivateKey string
	Passphrase string
	KnownHosts string
	Token      string
	Username   string
}

// GitCredLookup resolves one of the org's named credentials to its material.
// The daemon wires this at boot (mirroring SetTokenLookup for OAuth), reading
// from the per-tenant encrypted store; the tenant rides on ctx. Returns a
// zero GitCred (no error) when the account isn't configured, so the caller
// can give a clear "not connected" message.
type GitCredLookup func(ctx context.Context, account string) (GitCred, error)

var (
	credLookupMu sync.RWMutex
	credLookup   GitCredLookup
)

// SetGitCredLookup installs the credential resolver. Called once at daemon
// startup; nil in unit tests that don't exercise the org credential store
// (they pass inline ssh_private_key / token params instead).
func SetGitCredLookup(fn GitCredLookup) {
	credLookupMu.Lock()
	defer credLookupMu.Unlock()
	credLookup = fn
}

// bundledKnownHosts seeds host-key verification for the most common public
// forges so a user cloning github.com / gitlab.com / git.sr.ht over SSH
// doesn't have to paste a known_hosts entry. These are the providers'
// published, stable ed25519 host keys. A credential's own known_hosts is
// appended to this set (both are consulted), so self-hosted forges and key
// rotations are handled by configuring known_hosts on the credential —
// never by disabling verification.
const bundledKnownHosts = `github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
gitlab.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAfuCHKVTjquxvt6CM6tdG4SLp1Btn/nOeHHE5UOzRdf
git.sr.ht ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMZvRd4EtM7R+IHVMWmDkVU3VLQTSwQDSAvW0t2Tkj60
`

// sshURLParts reports whether rawURL is an SSH remote and, if so, its user
// and host. It recognizes both the ssh:// scheme and scp-like syntax
// (git@host:path). user defaults to "git" when the URL omits it.
func sshURLParts(rawURL string) (user, host string, isSSH bool) {
	raw := strings.TrimSpace(rawURL)
	if strings.HasPrefix(raw, "ssh://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", false
		}
		user = "git"
		if u.User != nil && u.User.Username() != "" {
			user = u.User.Username()
		}
		return user, u.Hostname(), true
	}
	if !strings.Contains(raw, "://") {
		if h, ok := scpLikeHost(raw); ok {
			user = "git"
			if at := strings.Index(raw, "@"); at >= 0 && at < strings.Index(raw, ":") {
				user = raw[:at]
			}
			return user, h, true
		}
	}
	return "", "", false
}

// IsSSHURL reports whether rawURL is a git-over-SSH remote — either the
// ssh:// scheme or scp-like syntax (git@host:path). Exported for callers
// outside this package that must reject non-SSH remotes up front (the
// workspace git mirror, which is SSH-key-only by design).
func IsSSHURL(rawURL string) bool {
	_, _, ok := sshURLParts(rawURL)
	return ok
}

// SSHAuth builds public-key auth plus strict host-key verification for an
// SSH git remote. It is the SSH half of authForURL, split out so callers
// that hold credential material directly — rather than a core.Job — reuse
// the same key parsing, bundled known_hosts and host-key-algorithm pinning
// instead of growing a second, weaker copy. The daemon's workspace mirror
// is the other caller.
//
// Returns an error for a non-SSH URL: this path has no unauthenticated or
// password fallback, so a caller that hands it an https:// remote has a
// bug, not a public repo.
func SSHAuth(rawURL, privateKey, passphrase, knownHosts string) (gogittransport.AuthMethod, error) {
	user, host, isSSH := sshURLParts(rawURL)
	if !isSSH {
		return nil, fmt.Errorf("not an SSH remote: %q (expected ssh://host/path or git@host:path)", rawURL)
	}
	auth, err := gitssh.NewPublicKeys(user, []byte(privateKey), passphrase)
	if err != nil {
		return nil, fmt.Errorf("parse SSH private key: %w (a passphrase-protected key needs its passphrase set on the credential)", err)
	}
	db, err := hostKeyDB(knownHosts)
	if err != nil {
		return nil, fmt.Errorf("host-key verification setup for %q: %w", host, err)
	}
	auth.HostKeyCallback = db.HostKeyCallback()
	hostPort := host + ":22"
	if u, perr := url.Parse(rawURL); perr == nil && u.Port() != "" {
		hostPort = host + ":" + u.Port()
	}
	auth.HostKeyAlgorithms = db.HostKeyAlgorithms(hostPort)
	return auth, nil
}

// resolveCred picks the credential for this clone: inline params (used by
// tests / programmatic callers) win; otherwise the named `account` (default
// "default") is looked up in the org's credential store via the wired hook.
func resolveCred(ctx context.Context, job core.Job) (GitCred, error) {
	c := GitCred{
		PrivateKey: paramOpt(job, "ssh_private_key"),
		Passphrase: paramOpt(job, "ssh_passphrase"),
		KnownHosts: paramOpt(job, "ssh_known_hosts"),
		Token:      paramOpt(job, "token"),
		Username:   paramOpt(job, "username"),
	}
	if c.PrivateKey != "" || c.Token != "" {
		return c, nil
	}
	account := params.StringDefault(job.Params, "account", "default")
	credLookupMu.RLock()
	fn := credLookup
	credLookupMu.RUnlock()
	if fn == nil {
		return GitCred{}, nil
	}
	return fn(ctx, account)
}

func paramOpt(job core.Job, key string) string {
	v, _ := params.StringOpt(job.Params, key)
	return v
}

// authForURL builds the go-git auth method for a repo URL:
//   - ssh:// or git@host: → public-key auth from the credential's SSH key
//     plus a strict host-key callback.
//   - https:// → HTTP basic auth from the credential's access token (PAT) —
//     or nil when there's no token (a public repo clones unauthenticated).
//
// Credential resolution mirrors the OAuth connectors' account model (see
// resolveCred). Returns (nil, nil) for an https URL with no token so the
// existing public-clone path is unchanged.
func authForURL(ctx context.Context, job core.Job, rawURL string) (gogittransport.AuthMethod, error) {
	cred, err := resolveCred(ctx, job)
	if err != nil {
		account := params.StringDefault(job.Params, "account", "default")
		return nil, fmt.Errorf("look up git credential %q: %w", account, err)
	}

	if IsSSHURL(rawURL) {
		if cred.PrivateKey == "" {
			return nil, fmt.Errorf("ssh clone needs an SSH key, but the selected git credential has none (add one under Git credentials, or use an https URL with an access token)")
		}
		return SSHAuth(rawURL, cred.PrivateKey, cred.Passphrase, cred.KnownHosts)
	}

	// https — authenticate with the access token (PAT) when present. GitHub,
	// GitLab, Bitbucket et al. accept the token as the basic-auth password
	// with any non-empty username; default to "git" when none is set.
	if cred.Token != "" {
		username := cred.Username
		if username == "" {
			username = "git"
		}
		return &githttp.BasicAuth{Username: username, Password: cred.Token}, nil
	}
	return nil, nil
}

// hostKeyDB builds a strict known-hosts database from the bundled defaults
// plus the credential's own known_hosts. It backs both the host-key callback
// and the host-key-algorithm constraint. We never fall back to an insecure
// "accept any host key" callback — over SSH that's silent MITM. An unknown
// host therefore fails the clone with a known-hosts error, which the user
// fixes by adding the host's key to the credential's known_hosts.
//
// NewKnownHostsDb reads the file eagerly into memory, so the temp file is
// removed as soon as the db is built.
func hostKeyDB(userKnownHosts string) (*knownhosts.HostKeyDB, error) {
	combined := bundledKnownHosts
	if s := strings.TrimSpace(userKnownHosts); s != "" {
		combined += s + "\n"
	}
	f, err := os.CreateTemp("", "dazyflow-known-hosts-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(combined); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return gitssh.NewKnownHostsDb(f.Name())
}

// hostKeyCallback returns just the verifier — used by tests; the clone path
// uses hostKeyDB directly so it can also constrain HostKeyAlgorithms.
func hostKeyCallback(userKnownHosts string) (gossh.HostKeyCallback, error) {
	db, err := hostKeyDB(userKnownHosts)
	if err != nil {
		return nil, err
	}
	return db.HostKeyCallback(), nil
}
