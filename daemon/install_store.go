package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
)

// Installed integrations and drops persist in the same encrypted store as
// OAuth provider creds, under their own pseudo-tenants. These records are
// public (the recipe / the drop source + the signature-derived trust tier — no
// secrets), but storing them here reuses the store's mem+pg backends instead
// of adding a new table; the slight over-encryption is harmless. Credentials
// stay in the provider store (saveProviderCreds), keyed by the same id —
// separate from the recipe, so a leaked record never carries a secret.
const (
	installedIntegrationsTenant = "_installed_integrations"
	installedDropsTenant        = "_installed_drops"
	installRecordPrefix         = "install/"
)

// storedIntegration is the persisted shape: the manifest recipe as transported
// (exact bytes — so a stored signature could be re-verified), the trust tier the
// install resolved to, and the source provenance (the resolved git commit is the
// immutable pin, so a force-moved tag is detectable across installs).
type storedIntegration struct {
	// Manifest holds the EXACT signed bytes (base64 in JSON, not inline
	// json.RawMessage — which the encoder compacts, mutating the bytes and
	// breaking signature re-verification over them).
	Manifest   []byte     `json:"manifest"`
	Tier       TrustTier  `json:"tier"`
	Provenance Provenance `json:"provenance,omitempty"`
	// Signature is the detached signature the install was verified with (nil for
	// an unsigned/community install). Persisted so boot can RE-verify against the
	// current keyring — a revoked key then downgrades the tier instead of the
	// stored verdict being replayed forever.
	Signature *Signature `json:"signature,omitempty"`
}

func saveInstalledIntegration(ctx context.Context, secrets *EncryptedSecrets, id string, manifestJSON []byte, tier TrustTier, prov Provenance, sig *Signature) error {
	payload, err := json.Marshal(storedIntegration{Manifest: manifestJSON, Tier: tier, Provenance: prov, Signature: sig})
	if err != nil {
		return fmt.Errorf("encode integration %q: %w", id, err)
	}
	return secrets.Put(ctx, installedIntegrationsTenant, installRecordPrefix+id, string(payload))
}

func listInstalledIntegrations(ctx context.Context, secrets *EncryptedSecrets) ([]storedIntegration, error) {
	names, err := secrets.List(ctx, installedIntegrationsTenant)
	if err != nil {
		return nil, err
	}
	out := make([]storedIntegration, 0, len(names))
	for _, n := range names {
		id, ok := strings.CutPrefix(n, installRecordPrefix)
		if !ok {
			continue
		}
		raw, err := secrets.Get(core.WithTenant(ctx, installedIntegrationsTenant), n)
		if err != nil {
			return nil, fmt.Errorf("load integration %q: %w", id, err)
		}
		var rec storedIntegration
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			return nil, fmt.Errorf("decode integration %q: %w", id, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

func removeInstalledIntegration(ctx context.Context, secrets *EncryptedSecrets, id string) error {
	return secrets.Delete(ctx, installedIntegrationsTenant, installRecordPrefix+id)
}

type storedDrop struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Source     string     `json:"source"`
	Tier       TrustTier  `json:"tier"`
	Provenance Provenance `json:"provenance,omitempty"`
	// Signature: see storedIntegration.Signature — persisted for boot re-verify.
	Signature *Signature `json:"signature,omitempty"`
}

func saveInstalledDrop(ctx context.Context, secrets *EncryptedSecrets, id, name, source string, tier TrustTier, prov Provenance, sig *Signature) error {
	payload, err := json.Marshal(storedDrop{ID: id, Name: name, Source: source, Tier: tier, Provenance: prov, Signature: sig})
	if err != nil {
		return fmt.Errorf("encode drop %q: %w", id, err)
	}
	return secrets.Put(ctx, installedDropsTenant, installRecordPrefix+id, string(payload))
}

func removeInstalledDrop(ctx context.Context, secrets *EncryptedSecrets, id string) error {
	return secrets.Delete(ctx, installedDropsTenant, installRecordPrefix+id)
}

func listInstalledDrops(ctx context.Context, secrets *EncryptedSecrets) ([]storedDrop, error) {
	names, err := secrets.List(ctx, installedDropsTenant)
	if err != nil {
		return nil, err
	}
	out := make([]storedDrop, 0, len(names))
	for _, n := range names {
		id, ok := strings.CutPrefix(n, installRecordPrefix)
		if !ok {
			continue
		}
		raw, err := secrets.Get(core.WithTenant(ctx, installedDropsTenant), n)
		if err != nil {
			return nil, fmt.Errorf("load drop %q: %w", id, err)
		}
		var d storedDrop
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			return nil, fmt.Errorf("decode drop %q: %w", id, err)
		}
		out = append(out, d)
	}
	return out, nil
}
