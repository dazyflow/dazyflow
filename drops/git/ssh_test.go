package git

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	"git.sr.ht/~klahr/hazyflow/core"
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

func TestSSHAuthForURL_InlineAndNonSSH(t *testing.T) {
	keyPEM := genKeyPEM(t)

	// https URL → no SSH auth (nil), https path untouched.
	auth, err := sshAuthForURL(t.Context(), core.Job{Params: map[string]any{}}, "https://github.com/x/y.git")
	if err != nil {
		t.Fatalf("https: %v", err)
	}
	if auth != nil {
		t.Errorf("https URL must yield nil ssh auth")
	}

	// ssh URL with an inline private key → non-nil auth, no lookup needed.
	job := core.Job{Params: map[string]any{"ssh_private_key": keyPEM}}
	auth, err = sshAuthForURL(t.Context(), job, "git@github.com:x/y.git")
	if err != nil {
		t.Fatalf("inline ssh: %v", err)
	}
	if auth == nil {
		t.Errorf("ssh URL with inline key must yield a non-nil auth method")
	}

	// ssh URL, no inline key, no lookup hook configured → clear error.
	SetSSHCredLookup(nil)
	if _, err := sshAuthForURL(t.Context(), core.Job{Params: map[string]any{}}, "git@github.com:x/y.git"); err == nil {
		t.Errorf("ssh URL without a credential must error")
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
