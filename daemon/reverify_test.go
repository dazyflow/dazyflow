package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

)

// widgetDropSrc is a verified-tier drop with no connection prerequisites, so it
// installs without an integration and restores without dependency setup.
const widgetDropSrc = `export default { manifest: {
	id: "widget", version: "1.0.0", label: "Widget",
	summary: "A widget.",
	outputs: [{ port: "out" }],
	examples: [{ title: "x", params: {} }]
}, run() { return { out: 1 }; } };`

func dropTier(in *Installer, id string) (TrustTier, bool) {
	for _, d := range in.InstalledDrops() {
		if d.ID == id {
			return d.Tier, true
		}
	}
	return "", false
}

// Restore re-verifies the persisted signature against the CURRENT keyring:
// the same keyring preserves the tier, a keyring missing the key (revocation)
// downgrades to community, and a revoked id is skipped entirely.
func TestInstaller_RestoreReverifiesTier(t *testing.T) {
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sig := &Signature{KeyID: "pub", Sig: ed25519.Sign(priv, []byte(widgetDropSrc))}
	trusting := NewKeyring(TrustedKey{ID: "pub", Publisher: "P", Tier: TierVerified, PublicKey: pub})

	secrets := newTestSecrets(t)
	newInstaller := func(kr *Keyring) *Installer {
		return NewInstaller(NewOAuthRegistry("https://app.example.test", secrets), testScriptedCatalog(t), secrets, kr)
	}

	// Install at verified tier (persists source + signature).
	in := newInstaller(trusting)
	if _, tier, err := in.InstallDrop(ctx, "widget.ts", widgetDropSrc, sig, Provenance{}); err != nil || tier != TierVerified {
		t.Fatalf("install: tier=%v err=%v (want verified)", tier, err)
	}

	// Same keyring → tier preserved.
	in2 := newInstaller(trusting)
	if _, errs := in2.Restore(ctx); len(errs) > 0 {
		t.Fatalf("restore errors: %v", errs)
	}
	if tier, ok := dropTier(in2, "widget"); !ok || tier != TierVerified {
		t.Errorf("same-keyring restore tier = (%v, %v), want verified", tier, ok)
	}

	// Key removed (revoked from HAZYFLOW_TRUSTED_KEYS) → downgraded to community,
	// still restored (the drop runs, just no longer trusted).
	in3 := newInstaller(NewKeyring())
	if _, errs := in3.Restore(ctx); len(errs) > 0 {
		t.Fatalf("restore errors: %v", errs)
	}
	if tier, ok := dropTier(in3, "widget"); !ok || tier != TierCommunity {
		t.Errorf("revoked-key restore tier = (%v, %v), want community", tier, ok)
	}

	// Kill switch: the id is on the revoked denylist → not restored at all.
	in4 := newInstaller(trusting)
	in4.SetRevoked([]string{"widget"})
	if _, errs := in4.Restore(ctx); len(errs) > 0 {
		t.Fatalf("restore errors: %v", errs)
	}
	if _, ok := dropTier(in4, "widget"); ok {
		t.Error("a revoked drop must not be restored")
	}
}

// A revoked id is also refused on a fresh install.
func TestInstaller_RevokedRefusesInstall(t *testing.T) {
	ctx := context.Background()
	in := NewInstaller(NewOAuthRegistry("https://app.example.test", nil), testScriptedCatalog(t), nil, nil)
	in.SetRevoked([]string{"widget"})
	if _, _, err := in.InstallDrop(ctx, "widget.ts", widgetDropSrc, nil, Provenance{}); err == nil {
		t.Fatal("installing a revoked drop should be refused")
	}
}
