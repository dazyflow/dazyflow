package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

)

// A drop that depends on the (third-party, non-reserved) "acme" integration.
const acmeDropSrc = `export default { manifest: {
	id: "acme_sync", version: "1.0.0", label: "Acme sync",
	summary: "Sync records from Acme.",
	requiresConnections: [{ kind: "oauth", name: "acme" }],
	outputs: [{ port: "out" }],
	examples: [{ title: "x", params: {} }]
}, async run(ctx) { return { out: "synced" }; } };`

func newTestSecrets(t *testing.T) *EncryptedSecrets {
	t.Helper()
	es, err := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("encrypted secrets: %v", err)
	}
	return es
}

// The headline flow: a drop can't be installed until its prerequisite
// integration is, and installing the integration registers the provider.
func TestInstaller_IntegrationGatesDrop(t *testing.T) {
	ctx := context.Background()
	oauth := NewOAuthRegistry("https://app.example.com", nil)
	cat := testScriptedCatalog(t)
	in := NewInstaller(oauth, cat, nil, nil) // no persistence/keyring needed for the gate

	// 1. The drop before its acme integration → rejected by the gate.
	if _, _, err := in.InstallDrop(ctx, "gmail.ts", acmeDropSrc, nil, Provenance{}); err == nil {
		t.Fatal("expected drop install to be gated on the acme integration")
	}
	if _, ok := cat.Get("acme_sync"); ok {
		t.Error("gated drop should not be in the catalog")
	}

	// 2. Install the acme integration → provider registered, support recorded.
	if _, _, err := in.InstallIntegration(ctx, []byte(acmeIntegrationJSON), map[string]string{"client_id": "cid", "client_secret": "sec"}, nil, Provenance{}); err != nil {
		t.Fatalf("install integration: %v", err)
	}
	if !in.IntegrationInstalled("acme") {
		t.Error("acme not recorded as installed")
	}
	if _, ok := oauth.Provider("acme"); !ok {
		t.Error("integration install did not register the OAuth provider")
	}

	// 3. Now the drop installs and lands in the catalog.
	if _, _, err := in.InstallDrop(ctx, "gmail.ts", acmeDropSrc, nil, Provenance{}); err != nil {
		t.Fatalf("drop install after integration: %v", err)
	}
	if _, ok := cat.Get("acme_sync"); !ok {
		t.Error("drop not in the catalog after a satisfied install")
	}
}

// A drop with no integration dependency installs with no prerequisite.
func TestInstaller_DependencyFreeDropInstallsFreely(t *testing.T) {
	in := NewInstaller(NewOAuthRegistry("https://x", nil), testScriptedCatalog(t), nil, nil)
	const httpDrop = `export default { manifest: {
		id: "ping", version: "1.0.0", label: "Ping", summary: "ping a url.",
		outputs: [{ port: "out" }], examples: [{ title: "x", params: {} }]
	}, async run() { return {}; } };`
	if _, _, err := in.InstallDrop(context.Background(), "ping.ts", httpDrop, nil, Provenance{}); err != nil {
		t.Fatalf("dependency-free drop should install: %v", err)
	}
}

// A secret-based requirement is satisfied per-node, not gated on an installed
// integration — so it doesn't block install.
func TestInstaller_SecretConnectionNotGated(t *testing.T) {
	in := NewInstaller(NewOAuthRegistry("https://x", nil), testScriptedCatalog(t), nil, nil)
	const stripeDrop = `export default { manifest: {
		id: "stripe_charge", version: "1.0.0", label: "Stripe charge",
		summary: "Create a charge.",
		requiresConnections: [{ kind: "secret", name: "STRIPE_KEY" }],
		outputs: [{ port: "out" }], examples: [{ title: "x", params: {} }]
	}, async run() { return {}; } };`
	if _, _, err := in.InstallDrop(context.Background(), "stripe.ts", stripeDrop, nil, Provenance{}); err != nil {
		t.Fatalf("secret-based drop should install without an integration: %v", err)
	}
}

// A signature determines the install's trust tier: Hazy's key → official, no
// signature → community, and a forged signature claiming a trusted key is
// rejected outright.
func TestInstaller_SignatureDerivesTier(t *testing.T) {
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kr := NewKeyring(TrustedKey{ID: "hazy", Publisher: "Hazy", Tier: TierOfficial, PublicKey: pub})
	secrets := newTestSecrets(t)
	in := NewInstaller(NewOAuthRegistry("https://app.example.com", secrets), testScriptedCatalog(t), secrets, kr)

	manifest := []byte(acmeIntegrationJSON)
	creds := map[string]string{"client_id": "c", "client_secret": "s"}

	// Signed by Hazy's key → official.
	sig := &Signature{KeyID: "hazy", Sig: ed25519.Sign(priv, manifest)}
	if _, tier, err := in.InstallIntegration(ctx, manifest, creds, sig, Provenance{}); err != nil || tier != TierOfficial {
		t.Fatalf("official install: tier=%v err=%v", tier, err)
	}

	// Unsigned → community.
	if _, tier, err := in.InstallIntegration(ctx, manifest, creds, nil, Provenance{}); err != nil || tier != TierCommunity {
		t.Errorf("unsigned install: tier=%v err=%v", tier, err)
	}

	// Forged (claims Hazy's keyID, signed by another key) → rejected before any
	// side effect.
	_, evil, _ := ed25519.GenerateKey(rand.Reader)
	forged := &Signature{KeyID: "hazy", Sig: ed25519.Sign(evil, manifest)}
	if _, _, err := in.InstallIntegration(ctx, manifest, creds, forged, Provenance{}); err == nil {
		t.Error("forged signature should be rejected at install")
	}
}

// A built-in provider id (google, slack, …) is a reserved namespace: an
// unsigned manifest may not claim it (that would let a community integration
// shadow the real provider and redirect the OAuth back-channel), but a signed
// official manifest may — which is exactly how the real Google integration
// installs.
func TestInstaller_ReservedIDRequiresTrust(t *testing.T) {
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kr := NewKeyring(TrustedKey{ID: "hazy", Publisher: "Hazy", Tier: TierOfficial, PublicKey: pub})
	secrets := newTestSecrets(t)
	in := NewInstaller(NewOAuthRegistry("https://app.example.com", secrets), testScriptedCatalog(t), secrets, kr)
	creds := map[string]string{"client_id": "c", "client_secret": "s"}

	// Unsigned (community) manifest claiming the reserved "google" id → refused,
	// with no provider registered.
	if _, _, err := in.InstallIntegration(ctx, []byte(googleIntegrationJSON), creds, nil, Provenance{}); err == nil {
		t.Fatal(`unsigned manifest must not be allowed to claim the reserved "google" id`)
	}
	if _, ok := in.oauth.Provider("google"); ok {
		t.Error("reserved provider was registered despite a refused install")
	}

	// Signed by Hazy's key (official) → the same id is allowed.
	sig := &Signature{KeyID: "hazy", Sig: ed25519.Sign(priv, []byte(googleIntegrationJSON))}
	if _, tier, err := in.InstallIntegration(ctx, []byte(googleIntegrationJSON), creds, sig, Provenance{}); err != nil || tier != TierOfficial {
		t.Fatalf("signed reserved-id install: tier=%v err=%v", tier, err)
	}
	if _, ok := in.oauth.Provider("google"); !ok {
		t.Error("signed reserved-id install did not register the provider")
	}
}

// Persistence: installs survive a "restart" — a fresh registry + catalog
// backed by the same store come back via Restore, provider creds included.
func TestInstaller_PersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	secrets := newTestSecrets(t) // the persistence that "survives" the restart

	// --- first boot: install acme + the dependent drop ---
	in1 := NewInstaller(NewOAuthRegistry("https://app.example.com", secrets), testScriptedCatalog(t), secrets, nil)
	if _, _, err := in1.InstallIntegration(ctx, []byte(acmeIntegrationJSON), map[string]string{"client_id": "cid", "client_secret": "sec"}, nil, Provenance{}); err != nil {
		t.Fatalf("install integration: %v", err)
	}
	if _, _, err := in1.InstallDrop(ctx, "gmail.ts", acmeDropSrc, nil, Provenance{}); err != nil {
		t.Fatalf("install drop: %v", err)
	}

	// --- restart: fresh registry + catalog, same store ---
	oauth2 := NewOAuthRegistry("https://app.example.com", secrets)
	cat2 := testScriptedCatalog(t)
	in2 := NewInstaller(oauth2, cat2, secrets, nil)

	restored, errs := in2.Restore(ctx)
	if len(errs) != 0 {
		t.Fatalf("restore errors: %v", errs)
	}

	// The provider came back, with its persisted credentials.
	p, ok := oauth2.Provider("acme")
	if !ok {
		t.Fatal("provider not re-registered after restart")
	}
	if p.ClientID != "cid" || p.ClientSecret != "sec" {
		t.Errorf("credentials not restored: id=%q secret=%q", p.ClientID, p.ClientSecret)
	}
	// The integration is recorded (gate passes for new installs)…
	if !in2.IntegrationInstalled("acme") {
		t.Error("integration not recorded after restore")
	}
	// …and the drop is back in the catalog.
	if _, ok := cat2.Get("acme_sync"); !ok {
		t.Error("drop not restored to the catalog")
	}
	t.Logf("restored: %v", restored)
}
