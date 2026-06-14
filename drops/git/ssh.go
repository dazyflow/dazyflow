package git

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	gogittransport "github.com/go-git/go-git/v5/plumbing/transport"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/skeema/knownhosts"
	gossh "golang.org/x/crypto/ssh"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
)

// SSHCredLookup resolves one of the org's named SSH credentials to its key
// material. The daemon wires this at boot (mirroring SetTokenLookup for
// OAuth), reading from the per-tenant encrypted store; the tenant rides on
// ctx. Returns empty strings (no error) when the account isn't configured,
// so the caller can give a clear "not connected" message.
type SSHCredLookup func(ctx context.Context, account string) (privateKeyPEM, passphrase, knownHosts string, err error)

var (
	sshLookupMu sync.RWMutex
	sshLookup   SSHCredLookup
)

// SetSSHCredLookup installs the credential resolver. Called once at daemon
// startup; nil in unit tests that don't exercise the org credential store
// (they pass an inline ssh_private_key param instead).
func SetSSHCredLookup(fn SSHCredLookup) {
	sshLookupMu.Lock()
	defer sshLookupMu.Unlock()
	sshLookup = fn
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
// (git@host:path). user defaults to "git" when the URL omits it — the near-
// universal convention for forge SSH access.
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

// sshAuthForURL builds the go-git auth method for an SSH-scheme repo URL:
// the public-key signer from the resolved credential plus a host-key
// callback seeded from the bundled known-hosts and the credential's own.
// Returns (nil, nil) for non-SSH URLs so the HTTPS path is untouched.
//
// Credential resolution mirrors the OAuth connectors' account model: an
// inline `ssh_private_key` param (used by tests / programmatic callers)
// wins; otherwise the named `account` (default "default") is looked up in
// the org's credential store via the wired hook.
func sshAuthForURL(ctx context.Context, job core.Job, rawURL string) (gogittransport.AuthMethod, error) {
	user, host, isSSH := sshURLParts(rawURL)
	if !isSSH {
		return nil, nil
	}

	privateKey, _ := params.StringOpt(job.Params, "ssh_private_key")
	passphrase, _ := params.StringOpt(job.Params, "ssh_passphrase")
	knownHosts, _ := params.StringOpt(job.Params, "ssh_known_hosts")

	if privateKey == "" {
		account := params.StringDefault(job.Params, "account", "default")
		sshLookupMu.RLock()
		fn := sshLookup
		sshLookupMu.RUnlock()
		if fn == nil {
			return nil, fmt.Errorf("ssh clone needs an SSH credential, but none is configured (add one under Git SSH credentials, or use an https URL)")
		}
		var err error
		privateKey, passphrase, knownHosts, err = fn(ctx, account)
		if err != nil {
			return nil, fmt.Errorf("look up SSH credential %q: %w", account, err)
		}
		if privateKey == "" {
			return nil, fmt.Errorf("SSH credential %q is not configured for this organization", account)
		}
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
	// Constrain the negotiated host-key algorithm to the ones we have an
	// entry for. Without this the client accepts whatever type the server
	// prefers (often RSA/ECDSA), which then fails our ed25519-only entry as
	// a "key mismatch" even though the host IS known. HostKeyAlgorithms is
	// empty for an unknown host — the callback still rejects it.
	hostPort := host + ":22"
	if u, perr := url.Parse(rawURL); perr == nil && u.Port() != "" {
		hostPort = host + ":" + u.Port()
	}
	auth.HostKeyAlgorithms = db.HostKeyAlgorithms(hostPort)
	return auth, nil
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
	f, err := os.CreateTemp("", "hazyflow-known-hosts-*")
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
