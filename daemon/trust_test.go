package daemon

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestKeyring_Verify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kr := NewKeyring(TrustedKey{ID: "hazy", Publisher: "Hazy", Tier: TierOfficial, PublicKey: pub})
	content := []byte("artifact bytes")
	good := &Signature{KeyID: "hazy", Sig: ed25519.Sign(priv, content)}

	// Valid signature by a trusted official key → official, with publisher.
	if tier, publisher, err := kr.Verify(content, good); err != nil || tier != TierOfficial || publisher != "Hazy" {
		t.Errorf("valid official: tier=%v publisher=%q err=%v", tier, publisher, err)
	}
	// No signature → community (allowed, lowest trust).
	if tier, _, err := kr.Verify(content, nil); err != nil || tier != TierCommunity {
		t.Errorf("unsigned: tier=%v err=%v", tier, err)
	}
	// Signed by a key we don't know → community (can't vouch).
	if tier, _, err := kr.Verify(content, &Signature{KeyID: "stranger", Sig: good.Sig}); err != nil || tier != TierCommunity {
		t.Errorf("unknown key: tier=%v err=%v", tier, err)
	}
	// Tampered content under a trusted keyID → rejected.
	if _, _, err := kr.Verify([]byte("tampered"), good); err == nil {
		t.Error("tampered content under a trusted keyID must be rejected")
	}
	// Forged: claims the official keyID but signed by a different key → rejected.
	// A forger can't reach "official" without Hazy's private key.
	_, evil, _ := ed25519.GenerateKey(rand.Reader)
	forged := &Signature{KeyID: "hazy", Sig: ed25519.Sign(evil, content)}
	if _, _, err := kr.Verify(content, forged); err == nil {
		t.Error("forged signature claiming a trusted key must be rejected")
	}
}
