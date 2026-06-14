package daemon

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	"git.sr.ht/~klahr/hazyflow/core"
)

func testEncryptedSecrets(t *testing.T) *EncryptedSecrets {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	es, err := NewEncryptedSecrets(key, NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	return es
}

// testSSHKeyPEM generates a fresh, unencrypted OpenSSH ed25519 private key.
func testSSHKeyPEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return string(pem.EncodeToMemory(block))
}

func TestGitSSHCredential_RoundTrip(t *testing.T) {
	es := testEncryptedSecrets(t)
	ctx := core.WithTenant(t.Context(), "acme")
	keyPEM := testSSHKeyPEM(t)

	// Put two named credentials, one with a known_hosts entry.
	if err := putGitSSHCredential(ctx, es, "acme", "github-deploy", keyPEM, "", "github.com ssh-ed25519 AAAA"); err != nil {
		t.Fatalf("put github-deploy: %v", err)
	}
	if err := putGitSSHCredential(ctx, es, "acme", "default", keyPEM, "", ""); err != nil {
		t.Fatalf("put default: %v", err)
	}

	creds, err := listGitSSHCredentials(ctx, es, "acme")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(creds) != 2 {
		t.Fatalf("len(creds) = %d, want 2 (%+v)", len(creds), creds)
	}
	// Sorted: "default" before "github-deploy".
	if creds[0].Account != "default" || creds[1].Account != "github-deploy" {
		t.Fatalf("accounts = %q,%q", creds[0].Account, creds[1].Account)
	}
	if !creds[1].HasKnownHosts {
		t.Errorf("github-deploy should report HasKnownHosts")
	}
	if creds[0].HasKnownHosts {
		t.Errorf("default should not report HasKnownHosts")
	}

	// Lookup returns the stored material.
	gotKey, gotPass, gotHosts, err := es.LookupGitSSHCredential(ctx, "github-deploy")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if gotPass != "" {
		t.Errorf("passphrase = %q, want empty", gotPass)
	}
	if gotHosts == "" {
		t.Errorf("known_hosts should be returned")
	}
	if _, perr := gossh.ParsePrivateKey([]byte(gotKey)); perr != nil {
		t.Errorf("looked-up key doesn't parse: %v", perr)
	}

	// Credentials must be hidden from the user-facing org secrets list.
	names, err := es.ListScoped(ctx, "acme", "", ScopeTenant)
	if err != nil {
		t.Fatalf("ListScoped: %v", err)
	}
	for _, n := range names {
		if n == "gitssh.default.private_key" || n == "gitssh.github-deploy.private_key" {
			t.Errorf("gitssh credential %q leaked into the org secrets list", n)
		}
	}

	// Delete removes all fields.
	if err := deleteGitSSHCredential(ctx, es, "acme", "github-deploy"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	creds, _ = listGitSSHCredentials(ctx, es, "acme")
	if len(creds) != 1 || creds[0].Account != "default" {
		t.Fatalf("after delete, creds = %+v, want only default", creds)
	}
	// Lookup of a deleted account returns empty, not an error.
	k, _, _, err := es.LookupGitSSHCredential(ctx, "github-deploy")
	if err != nil || k != "" {
		t.Errorf("lookup deleted = (%q, %v), want empty,nil", k, err)
	}
}

func TestGitSSHCredential_Validation(t *testing.T) {
	es := testEncryptedSecrets(t)
	ctx := core.WithTenant(t.Context(), "acme")

	// Bad account name.
	if err := putGitSSHCredential(ctx, es, "acme", "bad name!", testSSHKeyPEM(t), "", ""); err == nil {
		t.Errorf("expected error for invalid account name")
	}
	// Garbage key.
	if err := putGitSSHCredential(ctx, es, "acme", "x", "not a key", "", ""); err == nil {
		t.Errorf("expected error for unparseable private key")
	}
	// Empty key.
	if err := putGitSSHCredential(ctx, es, "acme", "x", "", "", ""); err == nil {
		t.Errorf("expected error for empty private key")
	}
}
