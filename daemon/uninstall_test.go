package daemon

import (
	"context"
	"strings"
	"testing"

)

func TestInstaller_Uninstall(t *testing.T) {
	ctx := context.Background()
	secrets := newTestSecrets(t)
	oauth := NewOAuthRegistry("https://app.example.test", secrets)
	in := NewInstaller(oauth, testScriptedCatalog(t), secrets, nil)

	// Install acme + the drop that depends on it.
	if _, _, err := in.InstallIntegration(ctx, []byte(acmeIntegrationJSON),
		map[string]string{"client_id": "cid", "client_secret": "sec"}, nil, Provenance{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := in.InstallDrop(ctx, "gmail.ts", acmeDropSrc, nil, Provenance{}); err != nil {
		t.Fatal(err)
	}

	// Uninstalling acme is refused while the acme drop depends on it.
	if err := in.UninstallIntegration(ctx, "acme"); err == nil {
		t.Fatal("uninstalling a depended-on integration should be refused")
	}
	if !in.IntegrationInstalled("acme") {
		t.Error("acme should still be installed after a refused uninstall")
	}

	// Uninstall the drop → gone from the catalog.
	if err := in.UninstallDrop(ctx, "acme_sync"); err != nil {
		t.Fatalf("uninstall drop: %v", err)
	}
	if _, ok := in.drops.Get("acme_sync"); ok {
		t.Error("drop still in catalog after uninstall")
	}

	// Now acme uninstalls: provider unregistered, support record cleared.
	if err := in.UninstallIntegration(ctx, "acme"); err != nil {
		t.Fatalf("uninstall integration: %v", err)
	}
	if in.IntegrationInstalled("acme") {
		t.Error("acme still recorded after uninstall")
	}
	if _, ok := oauth.Provider("acme"); ok {
		t.Error("provider still registered after uninstall")
	}
}

// acme_sync@2.0.0 drops the acme requirement the 1.0.0 had. With both versions
// installed, the dependency gate must still see 1.0.0's requirement — checking
// only the latest version would wrongly allow uninstalling acme and break a
// flow pinned to 1.0.0.
const acmeDropSrcV2 = `export default { manifest: {
	id: "acme_sync", version: "2.0.0", label: "Acme sync",
	summary: "Sync records from Acme.",
	outputs: [{ port: "out" }],
	examples: [{ title: "x", params: {} }]
}, async run(ctx) { return { out: "synced" }; } };`

func TestInstaller_DependencyGate_AcrossVersions(t *testing.T) {
	ctx := context.Background()
	secrets := newTestSecrets(t)
	oauth := NewOAuthRegistry("https://app.example.test", secrets)
	in := NewInstaller(oauth, testScriptedCatalog(t), secrets, nil)

	if _, _, err := in.InstallIntegration(ctx, []byte(acmeIntegrationJSON),
		map[string]string{"client_id": "cid", "client_secret": "sec"}, nil, Provenance{}); err != nil {
		t.Fatal(err)
	}
	// Install 1.0.0 (needs acme), then 2.0.0 (doesn't). Both coexist in the
	// catalog; the latest no longer references acme.
	if _, _, err := in.InstallDrop(ctx, "acme_v1.ts", acmeDropSrc, nil, Provenance{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := in.InstallDrop(ctx, "acme_v2.ts", acmeDropSrcV2, nil, Provenance{}); err != nil {
		t.Fatal(err)
	}

	// The 1.0.0 pin still depends on acme, so the uninstall is refused and the
	// error names the exact version that blocks it.
	err := in.UninstallIntegration(ctx, "acme")
	if err == nil {
		t.Fatal("uninstalling acme should be refused while acme_sync@1.0.0 still requires it")
	}
	if !strings.Contains(err.Error(), "acme_sync@1.0.0") {
		t.Errorf("error should name the pinned dependent version, got: %v", err)
	}
	if !in.IntegrationInstalled("acme") {
		t.Error("acme should still be installed after the refused uninstall")
	}
}

// Uninstall persists: a fresh Installer over the same store does not Restore the
// uninstalled items.
func TestInstaller_UninstallPersists(t *testing.T) {
	ctx := context.Background()
	secrets := newTestSecrets(t)

	in1 := NewInstaller(NewOAuthRegistry("https://app.example.test", secrets), testScriptedCatalog(t), secrets, nil)
	if _, _, err := in1.InstallIntegration(ctx, []byte(acmeIntegrationJSON),
		map[string]string{"client_id": "c", "client_secret": "s"}, nil, Provenance{}); err != nil {
		t.Fatal(err)
	}
	if err := in1.UninstallIntegration(ctx, "acme"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	// Restart: nothing should come back.
	in2 := NewInstaller(NewOAuthRegistry("https://app.example.test", secrets), testScriptedCatalog(t), secrets, nil)
	if restored, errs := in2.Restore(ctx); len(errs) != 0 {
		t.Fatalf("restore errors: %v", errs)
	} else if len(restored) != 0 {
		t.Errorf("uninstalled integration came back on restore: %v", restored)
	}
	if in2.IntegrationInstalled("acme") {
		t.Error("acme restored after it was uninstalled")
	}
}
