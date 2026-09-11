// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	"github.com/dazyflow/dazyflow/core"
)

func TestSSHCredential_RoundTrip(t *testing.T) {
	t.Parallel()
	es := testEncryptedSecrets(t)
	ctx := core.WithTenant(t.Context(), "acme")
	keyPEM := testSSHKeyPEM(t)

	if err := putSSHCredential(ctx, es, "acme", "web-1", sshCredInput{
		Host: "ssh.example.com", Username: "deploy", PrivateKey: keyPEM,
		Fingerprint: "SHA256:abc", Port: "2222",
	}); err != nil {
		t.Fatalf("put web-1: %v", err)
	}
	if err := putSSHCredential(ctx, es, "acme", "bank", sshCredInput{
		Host: "sftp.bank.example", Username: "acme", Password: "hunter2",
		KnownHosts: "sftp.bank.example ssh-ed25519 AAAA", Directory: "/incoming",
	}); err != nil {
		t.Fatalf("put bank: %v", err)
	}

	creds, err := listSSHCredentials(ctx, es, "acme")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(creds) != 2 {
		t.Fatalf("len(creds) = %d, want 2 (%+v)", len(creds), creds)
	}
	by := map[string]SSHCredential{}
	for _, c := range creds {
		by[c.Account] = c
	}
	// The address and login come back so a picker can tell two servers apart.
	if by["web-1"].Host != "ssh.example.com" || by["web-1"].Username != "deploy" {
		t.Errorf("web-1 listing lost its address or login: %+v", by["web-1"])
	}
	if by["bank"].Directory != "/incoming" {
		t.Errorf("bank listing lost its folder: %+v", by["bank"])
	}
	if !by["web-1"].HasSSHKey || by["web-1"].HasPassword {
		t.Errorf("web-1 auth flags wrong: %+v", by["web-1"])
	}
	if by["bank"].HasSSHKey || !by["bank"].HasPassword {
		t.Errorf("bank auth flags wrong: %+v", by["bank"])
	}
	// Either kind of pin counts as pinned — they are alternatives, not a pair.
	if !by["web-1"].HasHostKey || !by["bank"].HasHostKey {
		t.Errorf("host-key pins not reported: %+v %+v", by["web-1"], by["bank"])
	}

	cfg, err := es.LookupSSHCredential(ctx, "web-1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if cfg.Host != "ssh.example.com" || cfg.Username != "deploy" || cfg.Port != 2222 {
		t.Errorf("lookup returned %+v", cfg)
	}
	if strings.TrimSpace(cfg.PrivateKey) != strings.TrimSpace(keyPEM) {
		t.Error("lookup did not return the private key it stored")
	}
	if cfg.Fingerprint != "SHA256:abc" {
		t.Errorf("fingerprint = %q", cfg.Fingerprint)
	}

	// An unset port is the protocol's default, not zero: a Config with port 0
	// would dial nothing at all.
	if cfg, err := es.LookupSSHCredential(ctx, "bank"); err != nil || cfg.Port != 22 {
		t.Errorf("bank port = %d (err %v), want 22", cfg.Port, err)
	}
}

// A missing account is not an error — the drop turns it into "add it on the
// Servers page", which a storage error would drown out.
func TestLookupSSHCredential_UnknownAccountIsEmptyNotAnError(t *testing.T) {
	t.Parallel()
	es := testEncryptedSecrets(t)
	ctx := core.WithTenant(t.Context(), "acme")
	cfg, err := es.LookupSSHCredential(ctx, "never-configured")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if cfg.Host != "" {
		t.Errorf("lookup invented a server: %+v", cfg)
	}
}

func TestLookupSSHCredential_IsPerTenant(t *testing.T) {
	t.Parallel()
	es := testEncryptedSecrets(t)
	acme := core.WithTenant(t.Context(), "acme")
	if err := putSSHCredential(acme, es, "acme", "web-1", sshCredInput{
		Host: "ssh.acme.example", Username: "deploy", Password: "x",
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	other := core.WithTenant(t.Context(), "globex")
	cfg, err := es.LookupSSHCredential(other, "web-1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if cfg.Host != "" {
		t.Errorf("globex reached acme's server: %+v", cfg)
	}
}

func TestPutSSHCredential_Validation(t *testing.T) {
	t.Parallel()
	es := testEncryptedSecrets(t)
	ctx := core.WithTenant(t.Context(), "acme")
	good := sshCredInput{Host: "h", Username: "u", Password: "p"}

	for _, tc := range []struct {
		name    string
		account string
		in      sshCredInput
		want    string
	}{
		{"no host", "a", sshCredInput{Username: "u", Password: "p"}, "address"},
		{"no username", "a", sshCredInput{Host: "h", Password: "p"}, "username"},
		{"no way in", "a", sshCredInput{Host: "h", Username: "u"}, "password or an SSH private key"},
		{"bad port", "a", sshCredInput{Host: "h", Username: "u", Password: "p", Port: "nope"}, "port"},
		{"unparseable key", "a", sshCredInput{Host: "h", Username: "u", PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nnope\n"}, "doesn't parse"},
		// A dot would collide with the field separator in the storage name.
		{"dotted account", "a.b", good, "account name"},
		{"empty account", "", good, "account name"},
		{"slashed account", "a/b", good, "account name"},
	} {
		err := putSSHCredential(ctx, es, "acme", tc.account, tc.in)
		if err == nil {
			t.Errorf("%s: accepted, want rejected", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
}

// Saving over a credential must clear what the new one leaves out, or a
// password removed in the UI keeps letting the flow in.
func TestPutSSHCredential_ReplacingClearsWhatIsNoLongerSet(t *testing.T) {
	t.Parallel()
	es := testEncryptedSecrets(t)
	ctx := core.WithTenant(t.Context(), "acme")
	keyPEM := testSSHKeyPEM(t)

	if err := putSSHCredential(ctx, es, "acme", "web-1", sshCredInput{
		Host: "h", Username: "u", Password: "old-password", Directory: "/old",
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := putSSHCredential(ctx, es, "acme", "web-1", sshCredInput{
		Host: "h", Username: "u", PrivateKey: keyPEM,
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	cfg, err := es.LookupSSHCredential(ctx, "web-1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if cfg.Password != "" {
		t.Error("the replaced credential kept its old password")
	}
	if cfg.Directory != "" {
		t.Error("the replaced credential kept its old folder")
	}
}

func TestDeleteSSHCredential_RemovesEveryField(t *testing.T) {
	t.Parallel()
	es := testEncryptedSecrets(t)
	ctx := core.WithTenant(t.Context(), "acme")
	if err := putSSHCredential(ctx, es, "acme", "web-1", sshCredInput{
		Host: "h", Username: "u", Password: "p", Directory: "/d", Fingerprint: "SHA256:x",
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := deleteSSHCredential(ctx, es, "acme", "web-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	names, err := es.List(ctx, "acme")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, n := range names {
		if strings.HasPrefix(n, secretSSHCredPrefix) {
			t.Errorf("delete left %q behind", n)
		}
	}
	// Deleting again is not an error: the UI can retry a half-failed delete.
	if err := deleteSSHCredential(ctx, es, "acme", "web-1"); err != nil {
		t.Errorf("second delete: %v", err)
	}
}

// Without an encrypted store there is nowhere to keep a key, and every route
// has to say so rather than half-work.
func TestSSHCredsAPI_RefusesWithoutAStore(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t) // no EncryptedSecrets

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/ssh/credentials"},
		{"PUT", "/api/v1/ssh/credentials/acct"},
		{"DELETE", "/api/v1/ssh/credentials/acct"},
		{"POST", "/api/v1/ssh/credentials/acct/verify"},
		{"POST", "/api/v1/ssh/host-key"},
		{"POST", "/api/v1/ssh/keypair"},
	} {
		if rw := h.do(t, tc.method, tc.path, map[string]any{"host": "h"}); rw.Code != http.StatusNotImplemented {
			t.Errorf("%s %s without a store = %d, want 501", tc.method, tc.path, rw.Code)
		}
	}
}

// The guide's Generate button. The pair has to be usable by the two things that
// consume it: putSSHCredential parses the private half before storing it, and a
// server's authorized_keys takes the public half verbatim.
func TestSSHKeypairAPI_MakesAStorablePair(t *testing.T) {
	t.Parallel()
	h := newSecretsHarness(t)

	rw := h.do(t, "POST", "/api/v1/ssh/keypair", map[string]any{"comment": "dazyflow web-1"})
	if rw.Code != http.StatusOK {
		t.Fatalf("keypair = %d, want 200: %s", rw.Code, rw.Body.String())
	}
	var got struct {
		PrivateKey string `json:"private_key"`
		PublicKey  string `json:"public_key"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	signer, err := gossh.ParsePrivateKey([]byte(got.PrivateKey))
	if err != nil {
		t.Fatalf("private half does not parse: %v", err)
	}
	pub, comment, _, _, err := gossh.ParseAuthorizedKey([]byte(got.PublicKey))
	if err != nil {
		t.Fatalf("public half is not an authorized_keys line: %v", err)
	}
	if comment != "dazyflow web-1" {
		t.Errorf("comment = %q, want the one asked for", comment)
	}
	if gossh.FingerprintSHA256(pub) != gossh.FingerprintSHA256(signer.PublicKey()) {
		t.Error("the halves do not belong together")
	}

	ctx := core.WithTenant(t.Context(), "t")
	if err := putSSHCredential(ctx, h.gw.EncryptedSecrets, "t", "web-1", sshCredInput{
		Host: "h", Username: "u", PrivateKey: strings.TrimSpace(got.PrivateKey),
	}); err != nil {
		t.Errorf("a generated key is not storable: %v", err)
	}
}

// A scan dials an address of the caller's choosing, so a port that is not a
// port is refused before anything is opened.
func TestSSHHostKeyAPI_RefusesNonsense(t *testing.T) {
	t.Parallel()
	h := newSecretsHarness(t)

	if rw := h.do(t, "POST", "/api/v1/ssh/host-key", map[string]any{"host": "h", "port": "no"}); rw.Code != http.StatusBadRequest {
		t.Errorf("a non-numeric port = %d, want 400", rw.Code)
	}
	// An address nobody answers on is an answer, not a failure: ok:false so the
	// guide can say what happened next to the field.
	rw := h.do(t, "POST", "/api/v1/ssh/host-key", map[string]any{"host": "", "port": ""})
	if rw.Code != http.StatusOK {
		t.Fatalf("a blank host = %d, want 200 with ok:false", rw.Code)
	}
	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.OK || got.Error == "" {
		t.Errorf("blank host = %+v, want ok:false with a reason", got)
	}
}
