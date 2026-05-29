package daemon

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func TestParseTrustedKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(pub)

	// Valid official + verified.
	k, err := ParseTrustedKey("hazy:official:Hazy:" + b64)
	if err != nil || k.ID != "hazy" || k.Tier != TierOfficial || k.Publisher != "Hazy" {
		t.Fatalf("official parse: %+v err=%v", k, err)
	}
	if v, err := ParseTrustedKey("acme:verified:Acme Inc:" + b64); err != nil || v.Tier != TierVerified || v.Publisher != "Acme Inc" {
		t.Errorf("verified parse: %+v err=%v", v, err)
	}

	bad := map[string]string{
		"too few fields":   "hazy:official:" + b64,
		"community tier":   "x:community:X:" + b64,
		"unknown tier":     "x:bogus:X:" + b64,
		"empty id":         ":official:X:" + b64,
		"bad base64":       "x:official:X:!!!notbase64!!!",
		"wrong key length": "x:official:X:" + base64.StdEncoding.EncodeToString([]byte("short")),
	}
	for name, spec := range bad {
		if _, err := ParseTrustedKey(spec); err == nil {
			t.Errorf("%s: expected parse error for %q", name, spec)
		}
	}
}

// A keyring built from config specs verifies a drop signed by a configured key
// to that key's tier, and one bad spec doesn't disable the rest.
func TestLoadKeyring_VerifiesConfiguredKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(pub)

	kr, errs := LoadKeyring([]string{
		"hazy:official:Hazy:" + b64,
		"",            // skipped
		"broken:spec", // collected as error, doesn't abort
	})
	if len(errs) != 1 {
		t.Fatalf("want 1 error for the broken spec, got %v", errs)
	}
	if ids := kr.IDs(); len(ids) != 1 || ids[0] != "hazy" {
		t.Fatalf("keyring ids = %v, want [hazy]", ids)
	}

	content := []byte("an integration manifest")
	good := &Signature{KeyID: "hazy", Sig: ed25519.Sign(priv, content)}
	if tier, publisher, err := kr.Verify(content, good); err != nil || tier != TierOfficial || publisher != "Hazy" {
		t.Errorf("verify configured key: tier=%v publisher=%q err=%v", tier, publisher, err)
	}
	// A key id not in the configured ring → community.
	if tier, _, err := kr.Verify(content, &Signature{KeyID: "stranger", Sig: good.Sig}); err != nil || tier != TierCommunity {
		t.Errorf("unknown key: tier=%v err=%v", tier, err)
	}
}

// Empty config (the default) yields a keyring where everything is community.
func TestLoadKeyring_Empty(t *testing.T) {
	kr, errs := LoadKeyring([]string{""}) // strings.Split("", ";") shape
	if len(errs) != 0 || len(kr.IDs()) != 0 {
		t.Fatalf("empty config: errs=%v ids=%v", errs, kr.IDs())
	}
	if tier, _, err := kr.Verify([]byte("x"), &Signature{KeyID: "anything", Sig: []byte("sig")}); err != nil || tier != TierCommunity {
		t.Errorf("empty keyring: tier=%v err=%v", tier, err)
	}
}
