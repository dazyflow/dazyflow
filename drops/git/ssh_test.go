// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package git

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"strings"
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gossh "golang.org/x/crypto/ssh"

	"github.com/dazyflow/dazyflow/core"
)

func TestSSHURLParts(t *testing.T) {
	cases := []struct {
		url       string
		wantUser  string
		wantHost  string
		wantIsSSH bool
	}{
		{"git@github.com:example/repo.git", "git", "github.com", true},
		{"ssh://git@gitlab.com/example/repo.git", "git", "gitlab.com", true},
		{"ssh://deploy@internal:2222/x.git", "deploy", "internal", true},
		{"https://github.com/example/repo.git", "", "", false},
		{"/srv/repos/local.git", "", "", false},
	}
	for _, c := range cases {
		u, h, ok := sshURLParts(c.url)
		if ok != c.wantIsSSH || u != c.wantUser || h != c.wantHost {
			t.Errorf("sshURLParts(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.url, u, h, ok, c.wantUser, c.wantHost, c.wantIsSSH)
		}
	}
}

func TestHostKeyCallback_AcceptsBundledRejectsUnknown(t *testing.T) {
	cb, err := hostKeyCallback("")
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}
	addr := &net.TCPAddr{IP: net.ParseIP("140.82.112.3"), Port: 22}

	// Every bundled host key (github.com, gitlab.com, git.sr.ht) must verify
	// against its own host without the user supplying known_hosts.
	var ghKey gossh.PublicKey
	for _, line := range strings.Split(strings.TrimSpace(bundledKnownHosts), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), " ", 2)
		host := fields[0]
		key, _, _, _, perr := gossh.ParseAuthorizedKey([]byte(fields[1]))
		if perr != nil {
			t.Fatalf("parse bundled key for %s: %v", host, perr)
		}
		if err := cb(host+":22", addr, key); err != nil {
			t.Errorf("bundled %s key should verify, got: %v", host, err)
		}
		if host == "github.com" {
			ghKey = key
		}
	}

	// A different (random) key for github.com must be rejected — this is the
	// MITM-protection guarantee.
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	wrong, _ := gossh.NewPublicKey(pub)
	if err := cb("github.com:22", addr, wrong); err == nil {
		t.Errorf("a mismatched host key must be rejected")
	}

	// An unknown host (not bundled, no user known_hosts) must be rejected.
	if err := cb("unknown.example.com:22", addr, ghKey); err == nil {
		t.Errorf("an unknown host must be rejected")
	}
}

// TestHostKeyAlgorithms_ConstrainedForBundledHosts guards the "key mismatch"
// regression: the clone must advertise only the host-key algorithms we have
// an entry for (ssh-ed25519 for the bundled forges), so the server doesn't
// negotiate an RSA/ECDSA key that then fails our ed25519-only known_hosts.
func TestHostKeyAlgorithms_ConstrainedForBundledHosts(t *testing.T) {
	db, err := hostKeyDB("")
	if err != nil {
		t.Fatalf("hostKeyDB: %v", err)
	}
	for _, host := range []string{"github.com", "gitlab.com", "git.sr.ht"} {
		algos := db.HostKeyAlgorithms(host + ":22")
		if len(algos) == 0 {
			t.Errorf("%s: no host-key algorithms (clone would negotiate any type → key mismatch)", host)
			continue
		}
		found := false
		for _, a := range algos {
			if a == "ssh-ed25519" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: algorithms %v missing ssh-ed25519", host, algos)
		}
	}
	// Unknown host: empty (callback still rejects it).
	if a := db.HostKeyAlgorithms("nope.example.com:22"); len(a) != 0 {
		t.Errorf("unknown host should have no algorithms, got %v", a)
	}
}

func TestHostKeyCallback_HonorsUserKnownHosts(t *testing.T) {
	// A user-supplied known_hosts entry for a self-hosted forge.
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := gossh.NewPublicKey(pub)
	line := "git.internal " + key.Type() + " " + base64Key(key)

	cb, err := hostKeyCallback(line)
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}
	addr := &net.TCPAddr{IP: net.ParseIP("10.0.0.5"), Port: 22}
	if err := cb("git.internal:22", addr, key); err != nil {
		t.Errorf("user known_hosts entry should verify, got: %v", err)
	}
}

func base64Key(k gossh.PublicKey) string {
	return strings.TrimPrefix(strings.TrimSpace(string(gossh.MarshalAuthorizedKey(k))), k.Type()+" ")
}

func TestAuthForURL_SSHAndHTTPSAndPublic(t *testing.T) {
	SetGitCredLookup(nil) // tests use inline params, not the org store
	keyPEM := genKeyPEM(t)

	// Public https URL, no token → no auth (public clone path unchanged).
	auth, err := authForURL(t.Context(), core.Job{Params: map[string]any{}}, "https://github.com/x/y.git")
	if err != nil {
		t.Fatalf("public https: %v", err)
	}
	if auth != nil {
		t.Errorf("public https URL must yield nil auth")
	}

	// https URL + inline PAT → HTTP basic auth.
	job := core.Job{Params: map[string]any{"token": "ghp_abc", "username": "octocat"}}
	auth, err = authForURL(t.Context(), job, "https://github.com/x/y.git")
	if err != nil {
		t.Fatalf("https pat: %v", err)
	}
	ba, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("https+token auth = %T, want *http.BasicAuth", auth)
	}
	if ba.Username != "octocat" || ba.Password != "ghp_abc" {
		t.Errorf("basic auth = %q/%q, want octocat/ghp_abc", ba.Username, ba.Password)
	}

	// https URL + token, no username → defaults username to "git".
	auth, _ = authForURL(t.Context(), core.Job{Params: map[string]any{"token": "ghp_def"}}, "https://gitlab.com/x/y.git")
	if ba, ok := auth.(*githttp.BasicAuth); !ok || ba.Username != "git" {
		t.Errorf("default username not applied: %#v", auth)
	}

	// ssh URL with an inline private key → public-key auth.
	auth, err = authForURL(t.Context(), core.Job{Params: map[string]any{"ssh_private_key": keyPEM}}, "git@github.com:x/y.git")
	if err != nil {
		t.Fatalf("inline ssh: %v", err)
	}
	if auth == nil {
		t.Errorf("ssh URL with inline key must yield a non-nil auth method")
	}

	// ssh URL, no key available → clear error.
	if _, err := authForURL(t.Context(), core.Job{Params: map[string]any{}}, "git@github.com:x/y.git"); err == nil {
		t.Errorf("ssh URL without an SSH key must error")
	}
}

// TestSSHAuth covers the exported builder the workspace git mirror uses.
// authForURL delegates its SSH branch here, so this is the shared path: key
// parsing, the pinned host-key database, and the host-key-algorithm
// constraint that keeps a mirror push from silently accepting a new host key.
func TestSSHAuth(t *testing.T) {
	keyPEM := genKeyPEM(t)
	// A real known_hosts line for the self-hosted case — the parser rejects a
	// placeholder, so build one from a generated public key.
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, _ := gossh.NewPublicKey(pub)
	knownHosts := "git.internal " + hostKey.Type() + " " + base64Key(hostKey)

	for _, url := range []string{
		"git@github.com:acme/flows.git",
		"ssh://git@git.sr.ht/~acme/flows",
		"ssh://git@git.internal:2222/acme/flows.git",
	} {
		auth, err := SSHAuth(url, keyPEM, "", knownHosts)
		if err != nil {
			t.Errorf("SSHAuth(%q) = %v, want an auth method", url, err)
			continue
		}
		if auth == nil {
			t.Errorf("SSHAuth(%q) returned a nil auth method", url)
		}
	}

	// An https URL has no key-based path — this builder has no
	// unauthenticated or password fallback, so it must refuse rather than
	// hand back something that silently won't authenticate.
	if _, err := SSHAuth("https://github.com/acme/flows.git", keyPEM, "", ""); err == nil {
		t.Error("SSHAuth on an https URL should error")
	}
	// Garbage key material fails at parse time, where the message can say so.
	if _, err := SSHAuth("git@github.com:acme/flows.git", "not a key", "", ""); err == nil {
		t.Error("SSHAuth with an unparseable key should error")
	}
}

func TestIsSSHURL(t *testing.T) {
	for _, in := range []string{
		"git@github.com:acme/flows.git",
		"ssh://git@git.sr.ht/~acme/flows",
		"user@host:path",
	} {
		if !IsSSHURL(in) {
			t.Errorf("IsSSHURL(%q) = false, want true", in)
		}
	}
	for _, in := range []string{
		"https://github.com/acme/flows.git",
		"http://git.internal/acme/flows",
		"file:///srv/git/flows.git",
		"",
	} {
		if IsSSHURL(in) {
			t.Errorf("IsSSHURL(%q) = true, want false", in)
		}
	}
}

func genKeyPEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(pem.EncodeToMemory(block))
}
