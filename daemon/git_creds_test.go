// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	"git.sr.ht/~klahr/dazyflow/core"
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

func TestGitCredential_RoundTrip(t *testing.T) {
	es := testEncryptedSecrets(t)
	ctx := core.WithTenant(t.Context(), "acme")
	keyPEM := testSSHKeyPEM(t)

	// An SSH-only credential, an HTTPS-PAT-only credential, and one with both.
	if err := putGitCredential(ctx, es, "acme", "ssh-deploy", gitCredInput{PrivateKey: keyPEM, KnownHosts: "git.internal ssh-ed25519 AAAA"}); err != nil {
		t.Fatalf("put ssh-deploy: %v", err)
	}
	if err := putGitCredential(ctx, es, "acme", "github-pat", gitCredInput{Token: "ghp_xxx", Username: "octocat"}); err != nil {
		t.Fatalf("put github-pat: %v", err)
	}
	if err := putGitCredential(ctx, es, "acme", "both", gitCredInput{PrivateKey: keyPEM, Token: "ghp_yyy"}); err != nil {
		t.Fatalf("put both: %v", err)
	}

	creds, err := listGitCredentials(ctx, es, "acme")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(creds) != 3 {
		t.Fatalf("len(creds) = %d, want 3 (%+v)", len(creds), creds)
	}
	by := map[string]GitCredential{}
	for _, c := range creds {
		by[c.Account] = c
	}
	if !by["ssh-deploy"].HasSSHKey || by["ssh-deploy"].HasToken {
		t.Errorf("ssh-deploy flags wrong: %+v", by["ssh-deploy"])
	}
	if !by["ssh-deploy"].HasKnownHosts {
		t.Errorf("ssh-deploy should report HasKnownHosts")
	}
	if by["github-pat"].HasSSHKey || !by["github-pat"].HasToken {
		t.Errorf("github-pat flags wrong: %+v", by["github-pat"])
	}
	if by["github-pat"].Username != "octocat" {
		t.Errorf("github-pat username = %q, want octocat", by["github-pat"].Username)
	}
	if !by["both"].HasSSHKey || !by["both"].HasToken {
		t.Errorf("both flags wrong: %+v", by["both"])
	}

	// Lookup returns the material; the key parses, the token round-trips.
	rc, err := es.LookupGitCredential(ctx, "both")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if rc.Token != "ghp_yyy" {
		t.Errorf("token = %q, want ghp_yyy", rc.Token)
	}
	if _, perr := gossh.ParsePrivateKey([]byte(rc.PrivateKey)); perr != nil {
		t.Errorf("looked-up key doesn't parse: %v", perr)
	}

	// Hidden from the user-facing org secrets list.
	names, err := es.ListScoped(ctx, "acme", "", ScopeTenant)
	if err != nil {
		t.Fatalf("ListScoped: %v", err)
	}
	for _, n := range names {
		if len(n) >= 7 && n[:7] == "gitcred" {
			t.Errorf("gitcred credential %q leaked into the org secrets list", n)
		}
	}

	// Delete removes everything for the account.
	if err := deleteGitCredential(ctx, es, "acme", "both"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rc, _ = es.LookupGitCredential(ctx, "both")
	if rc.PrivateKey != "" || rc.Token != "" {
		t.Errorf("deleted credential still resolves: %+v", rc)
	}
}

func TestGitCredential_Validation(t *testing.T) {
	es := testEncryptedSecrets(t)
	ctx := core.WithTenant(t.Context(), "acme")

	// Neither key nor token → rejected.
	if err := putGitCredential(ctx, es, "acme", "empty", gitCredInput{}); err == nil {
		t.Errorf("expected error for a credential with no key and no token")
	}
	// Bad account name.
	if err := putGitCredential(ctx, es, "acme", "bad name!", gitCredInput{Token: "x"}); err == nil {
		t.Errorf("expected error for invalid account name")
	}
	// Garbage key.
	if err := putGitCredential(ctx, es, "acme", "x", gitCredInput{PrivateKey: "not a key"}); err == nil {
		t.Errorf("expected error for unparseable private key")
	}
	// Token-only is fine (PAT credential with no SSH key).
	if err := putGitCredential(ctx, es, "acme", "patonly", gitCredInput{Token: "ghp_z"}); err != nil {
		t.Errorf("token-only credential should be valid: %v", err)
	}
}
