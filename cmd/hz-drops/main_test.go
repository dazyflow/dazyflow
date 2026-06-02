package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/daemon"
)

// The end-to-end trust loop: keygen → sign → the daemon's keyring verifies the
// signature at the official tier over the exact signed bytes. This is the link
// between the signing tool and daemon/trust.go that nothing else covers.
func TestKeygenAndSign_VerifiesAsOfficial(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "hz-drops")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	// keygen
	keysDir := filepath.Join(dir, "keys")
	kg := exec.Command(bin, "keygen", "--id", "hazy-official", "--publisher", "Hazyflow", "--out", keysDir)
	if out, err := kg.CombinedOutput(); err != nil {
		t.Fatalf("keygen: %v\n%s", err, out)
	}
	trustedSpec := readTrimmed(t, filepath.Join(keysDir, "hazy-official.trustedkey"))

	// The printed spec parses and confers the official tier.
	tk, err := daemon.ParseTrustedKey(trustedSpec)
	if err != nil {
		t.Fatalf("trusted key spec %q does not parse: %v", trustedSpec, err)
	}
	if tk.Tier != daemon.TierOfficial || tk.Publisher != "Hazyflow" {
		t.Fatalf("trusted key = %+v, want tier=official publisher='Hazyflow'", tk)
	}

	// sign a sample artifact
	artifact := filepath.Join(dir, "drop.ts")
	const body = "export default { manifest: { id: 'x' }, run() {} };\n"
	if err := os.WriteFile(artifact, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sg := exec.Command(bin, "sign", "--key", filepath.Join(keysDir, "hazy-official.key"), "--id", "hazy-official", artifact)
	if out, err := sg.CombinedOutput(); err != nil {
		t.Fatalf("sign: %v\n%s", err, out)
	}

	// The .sig the daemon would read off the repo verifies at official tier over
	// the EXACT signed bytes.
	var sig daemon.Signature
	if err := json.Unmarshal(readBytes(t, artifact+".sig"), &sig); err != nil {
		t.Fatalf("decode .sig: %v", err)
	}
	kr := daemon.NewKeyring(tk)
	tier, publisher, err := kr.Verify([]byte(body), &sig)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if tier != daemon.TierOfficial || publisher != "Hazyflow" {
		t.Fatalf("verify tier=%q publisher=%q, want official/Hazyflow", tier, publisher)
	}

	// Tamper detection: a changed byte must fail verification, not downgrade.
	if _, _, err := kr.Verify([]byte("export default { manifest: { id: 'TAMPERED' } };"), &sig); err == nil {
		t.Error("verification should reject content that doesn't match the signature")
	}

	// The private key is not world-readable.
	info, err := os.Stat(filepath.Join(keysDir, "hazy-official.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("private key mode = %v, want 0600", info.Mode().Perm())
	}
}

func readTrimmed(t *testing.T, path string) string {
	t.Helper()
	return strings.TrimSpace(string(readBytes(t, path)))
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
