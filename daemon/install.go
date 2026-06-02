package daemon

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine/jsdrop"
)

// Provenance records where a git-installed artifact came from: the repo, the
// human ref (a tag), and the resolved commit. The commit is the immutable pin —
// a tag can be force-moved, a commit can't — so recording it makes an install
// reproducible and lets a moved tag be detected on the next install. The zero
// value means the artifact was installed from inline bytes (no git source).
type Provenance struct {
	Repo   string `json:"repo,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// fromGit reports whether this provenance carries a resolved commit pin.
func (p Provenance) fromGit() bool { return p.Commit != "" }

// Installer is the marketplace install orchestrator. It ties together the two
// installable artifact kinds and enforces the prerequisite between them:
//
//   - Installing an integration ("google") registers its provider and records
//     that the platform now supports it.
//   - Installing a drop ("gmail_send") is gated on its required integrations
//     being installed first — you can't install Gmail before Google.
//
// Every install is trust-verified: the artifact's exact bytes are checked
// against the keyring, yielding a signature-derived TrustTier (official /
// verified / community). A signature that claims a trusted key but fails is
// rejected. The tier is recorded with the install (it informs humans; it does
// NOT change the runtime sandbox).
//
// Installs persist (recipe + source + tier in the install-record store,
// credentials in the encrypted provider store); Restore re-applies them on
// boot. The transport (fetch repo@tag) and the admin API/GUI are separate
// layers. Scope is global for now.
type Installer struct {
	oauth   *OAuthRegistry
	drops   *jsdrop.Catalog
	secrets *EncryptedSecrets // persistence: install records + creds. Nil = in-memory only.
	keyring *Keyring          // trust verification

	// revoked is the operator kill switch: ids here are refused on install and
	// skipped on Restore, regardless of signature/tier. Set from boot config
	// (HAZYFLOW_REVOKED_INSTALLS) — the root of authority, like trusted keys, so
	// a compromised drop/integration can be permanently disabled without relying
	// on key rotation. Read-only after construction (no lock needed).
	revoked map[string]bool

	mu           sync.RWMutex
	integrations map[string]integrationState // id -> version + tier + provenance
	dropStates   map[string]dropState        // drop id -> tier + provenance
}

type integrationState struct {
	Version    string
	Tier       TrustTier
	Provenance Provenance
}

type dropState struct {
	Tier       TrustTier
	Provenance Provenance
}

// InstalledIntegration / InstalledDropInfo are the listing views (id + tier,
// plus the manifest for drops) the admin API returns.
type InstalledIntegration struct {
	ID         string     `json:"id"`
	Version    string     `json:"version"`
	Tier       TrustTier  `json:"tier"`
	Provenance Provenance `json:"provenance"`
}

type InstalledDropInfo struct {
	ID         string        `json:"id"`
	Tier       TrustTier     `json:"tier"`
	Provenance Provenance    `json:"provenance"`
	Manifest   core.Manifest `json:"manifest"`
}

func NewInstaller(oauth *OAuthRegistry, drops *jsdrop.Catalog, secrets *EncryptedSecrets, keyring *Keyring) *Installer {
	if keyring == nil {
		keyring = NewKeyring()
	}
	return &Installer{
		oauth:        oauth,
		drops:        drops,
		secrets:      secrets,
		keyring:      keyring,
		revoked:      map[string]bool{},
		integrations: map[string]integrationState{},
		dropStates:   map[string]dropState{},
	}
}

// SetRevoked installs the operator's kill-switch denylist of ids (drops and/or
// integrations). Called once at boot from config. Revoked ids are refused on
// install and skipped on Restore.
func (in *Installer) SetRevoked(ids []string) {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			m[id] = true
		}
	}
	in.revoked = m
}

// isRevoked reports whether an id is on the kill-switch denylist.
func (in *Installer) isRevoked(id string) bool { return in.revoked[id] }

// auditInstall logs a one-line record of a completed install, including the
// resolved commit pin when the artifact came from git.
func auditInstall(kind, id string, tier TrustTier, prov Provenance) {
	if prov.fromGit() {
		log.Printf("marketplace: installed %s %q (tier=%s) from %s@%s commit %s", kind, id, tier, prov.Repo, prov.Ref, prov.Commit)
	} else {
		log.Printf("marketplace: installed %s %q (tier=%s) from inline source", kind, id, tier)
	}
}

// warnIfTagMoved logs a warning when re-installing the same id from the same
// repo+ref resolves to a different commit than last time — the force-moved-tag
// signal. It reads the previous provenance from in-memory state (populated by a
// prior install or Restore).
func (in *Installer) warnIfTagMoved(kind, id string, prev, now Provenance) {
	if now.fromGit() && prev.fromGit() && prev.Repo == now.Repo && prev.Ref == now.Ref && prev.Commit != now.Commit {
		log.Printf("marketplace: WARNING %s %q ref %s@%s moved commit %s → %s (tag is not immutable)", kind, id, now.Repo, now.Ref, prev.Commit, now.Commit)
	}
}

// Verify exposes the keyring's verification (e.g. for the preview endpoint to
// show the trust tier before installing).
func (in *Installer) Verify(content []byte, sig *Signature) (TrustTier, string, error) {
	return in.keyring.Verify(content, sig)
}

// guardReservedID rejects a manifest that claims a reserved built-in provider id
// (google, slack, …) unless it carries a vouched signature. See the call site
// in InstallIntegration for why the id namespace, not the tier badge, is what
// has to be protected here.
func guardReservedID(id string, tier TrustTier) error {
	if providerDefault(id) != nil && !tier.Trusted() {
		return fmt.Errorf("install %q: id is a reserved built-in provider; only a signed (official or verified) integration may claim it", id)
	}
	return nil
}

// InstallIntegration verifies the manifest bytes, registers the provider
// (oauth2) from manifest + creds, persists everything, and records the
// integration so dependent drops install. Returns the parsed manifest and the
// signature-derived tier. A forged signature (claims a trusted key but fails)
// is rejected before any side effect.
func (in *Installer) InstallIntegration(ctx context.Context, manifestJSON []byte, creds map[string]string, sig *Signature, prov Provenance) (IntegrationManifest, TrustTier, error) {
	tier, _, err := in.keyring.Verify(manifestJSON, sig)
	if err != nil {
		return IntegrationManifest{}, "", err
	}
	m, err := LoadIntegrationManifest(manifestJSON)
	if err != nil {
		return IntegrationManifest{}, "", err
	}
	if in.isRevoked(m.ID) {
		return IntegrationManifest{}, "", fmt.Errorf("integration %q is revoked on this platform and cannot be installed", m.ID)
	}
	// Built-in provider ids (google, slack, …) are a reserved namespace. Provider
	// registration is last-write-wins, so an unsigned manifest declaring
	// id:"google" would silently shadow the built-in and could redirect the OAuth
	// back-channel (exchanging the real client_secret against an attacker's
	// tokenUrl). Only a signed, vouched manifest may claim a reserved id — which
	// is exactly how the official Google/Slack integrations install.
	if err := guardReservedID(m.ID, tier); err != nil {
		return IntegrationManifest{}, "", err
	}
	switch m.Auth.Kind {
	case "oauth2":
		if in.oauth == nil {
			return IntegrationManifest{}, "", fmt.Errorf("install %q: OAuth is not configured on this daemon", m.ID)
		}
		if err := RegisterFromManifest(in.oauth, m, creds["client_id"], creds["client_secret"]); err != nil {
			return IntegrationManifest{}, "", err
		}
		if in.secrets != nil {
			if err := saveProviderCreds(ctx, in.secrets, m.ID, providerCreds{
				ClientID:     creds["client_id"],
				ClientSecret: creds["client_secret"],
			}); err != nil {
				return IntegrationManifest{}, "", fmt.Errorf("persist credentials for %q: %w", m.ID, err)
			}
		}
	case "secret", "none":
		// Nothing to register — the credential (if any) is provided per-node.
	default:
		return IntegrationManifest{}, "", fmt.Errorf("install %q: unsupported auth.kind %q", m.ID, m.Auth.Kind)
	}
	in.mu.RLock()
	prev := in.integrations[m.ID].Provenance
	in.mu.RUnlock()
	in.warnIfTagMoved("integration", m.ID, prev, prov)

	if in.secrets != nil {
		if err := saveInstalledIntegration(ctx, in.secrets, m.ID, manifestJSON, tier, prov, sig); err != nil {
			return IntegrationManifest{}, "", fmt.Errorf("persist integration %q: %w", m.ID, err)
		}
	}
	in.mu.Lock()
	in.integrations[m.ID] = integrationState{Version: m.Version, Tier: tier, Provenance: prov}
	in.mu.Unlock()
	auditInstall("integration", m.ID, tier, prov)
	return m, tier, nil
}

// InstallDrop verifies the source, enforces integration prerequisites (the
// gate), adds the drop to the catalog, and persists it with its tier. A
// missing oauth integration is rejected; secret/other connection kinds are
// satisfied per-node, not gated here.
func (in *Installer) InstallDrop(ctx context.Context, name, source string, sig *Signature, prov Provenance) (core.Manifest, TrustTier, error) {
	tier, _, err := in.keyring.Verify([]byte(source), sig)
	if err != nil {
		return core.Manifest{}, "", err
	}
	man, err := in.drops.Inspect(name, source)
	if err != nil {
		return core.Manifest{}, "", err
	}
	if in.isRevoked(man.ID) {
		return core.Manifest{}, "", fmt.Errorf("drop %q is revoked on this platform and cannot be installed", man.ID)
	}
	for _, req := range man.RequiresConnections {
		if req.Kind == "oauth" && !in.IntegrationInstalled(req.Name) {
			return core.Manifest{}, "", fmt.Errorf("drop %q requires the %q integration, which is not installed", man.ID, req.Name)
		}
	}
	// Every drop — official or runtime-installed — executes out-of-process in
	// the Node drop host; the manifest we just read via Inspect came from that
	// same runtime, so register it directly with AddPrebuilt (no re-extraction).
	// Only official/verified-signed drops are trusted for the relaxed egress
	// default; an unsigned (community) install gets the deny-by-default policy.
	trusted := tier == TierOfficial || tier == TierVerified
	if _, _, err := in.drops.AddPrebuilt(name, source, man, true, trusted); err != nil {
		return core.Manifest{}, "", err
	}
	in.mu.RLock()
	prev := in.dropStates[man.ID].Provenance
	in.mu.RUnlock()
	in.warnIfTagMoved("drop", man.ID, prev, prov)

	if in.secrets != nil {
		if err := saveInstalledDrop(ctx, in.secrets, man.ID, name, source, tier, prov, sig); err != nil {
			return core.Manifest{}, "", fmt.Errorf("persist drop %q: %w", man.ID, err)
		}
	}
	in.mu.Lock()
	in.dropStates[man.ID] = dropState{Tier: tier, Provenance: prov}
	in.mu.Unlock()
	auditInstall("drop", man.ID, tier, prov)
	return man, tier, nil
}

// InspectDrop reads a drop's manifest from source without installing it — the
// capability-preview path. Routes through the catalog's Node manifest extractor
// (the same runtime that will execute the drop), so the preview reflects exactly
// what the daemon will gate on.
func (in *Installer) InspectDrop(name, source string) (core.Manifest, error) {
	return in.drops.Inspect(name, source)
}

// reverifyTier re-runs signature verification against the CURRENT keyring for a
// persisted install, so boot honors key revocation instead of replaying the
// stored verdict forever. ok=false means the stored signature no longer
// verifies at all (tampering / a forged trusted-key claim) — the caller skips
// the item. A downgrade (a now-unknown key dropping the artifact to community)
// is returned as the new effective tier.
func (in *Installer) reverifyTier(content []byte, sig *Signature, stored TrustTier, kind, id string) (TrustTier, bool) {
	tier, _, err := in.keyring.Verify(content, sig)
	if err != nil {
		log.Printf("install restore: %s %q signature no longer verifies (%v); refusing to restore", kind, id, err)
		return "", false
	}
	if tier != stored {
		log.Printf("install restore: %s %q tier %s → %s on re-verification (trusted-key set changed)", kind, id, stored, tier)
	}
	return tier, true
}

// Restore re-applies persisted installs on boot: re-registers each integration
// provider from its stored recipe + credentials, re-records it, then re-adds
// every persisted drop. Each record's signature is RE-VERIFIED against the
// current keyring rather than replaying the stored tier, so removing a key from
// HAZYFLOW_TRUSTED_KEYS downgrades (or, for a reserved id, refuses) that
// publisher's artifacts on the next boot. Revoked ids are skipped outright.
// Per-item errors don't fail the whole boot.
func (in *Installer) Restore(ctx context.Context) (restored []string, errs []error) {
	if in.secrets == nil {
		return nil, nil
	}
	integs, err := listInstalledIntegrations(ctx, in.secrets)
	if err != nil {
		return nil, []error{fmt.Errorf("list installed integrations: %w", err)}
	}
	for _, rec := range integs {
		m, err := LoadIntegrationManifest(rec.Manifest)
		if err != nil {
			errs = append(errs, fmt.Errorf("decode installed integration: %w", err))
			continue
		}
		if in.isRevoked(m.ID) {
			log.Printf("install restore: integration %q is revoked; skipping", m.ID)
			continue
		}
		tier, ok := in.reverifyTier(rec.Manifest, rec.Signature, rec.Tier, "integration", m.ID)
		if !ok {
			continue
		}
		if err := guardReservedID(m.ID, tier); err != nil {
			errs = append(errs, err)
			continue
		}
		if m.Auth.Kind == "oauth2" {
			c, err := loadProviderCreds(ctx, in.secrets, m.ID)
			if err != nil {
				errs = append(errs, fmt.Errorf("integration %q credentials: %w", m.ID, err))
				continue
			}
			if c == nil || c.ClientID == "" || c.ClientSecret == "" {
				errs = append(errs, fmt.Errorf("integration %q has no stored credentials; skipping", m.ID))
				continue
			}
			if in.oauth == nil {
				errs = append(errs, fmt.Errorf("integration %q needs OAuth, which is not configured", m.ID))
				continue
			}
			if err := RegisterFromManifest(in.oauth, m, c.ClientID, c.ClientSecret); err != nil {
				errs = append(errs, fmt.Errorf("integration %q: %w", m.ID, err))
				continue
			}
		}
		in.mu.Lock()
		in.integrations[m.ID] = integrationState{Version: m.Version, Tier: tier, Provenance: rec.Provenance}
		in.mu.Unlock()
		restored = append(restored, "integration:"+m.ID)
	}

	drops, err := listInstalledDrops(ctx, in.secrets)
	if err != nil {
		return restored, append(errs, fmt.Errorf("list installed drops: %w", err))
	}
	for _, d := range drops {
		if in.isRevoked(d.ID) {
			log.Printf("install restore: drop %q is revoked; skipping", d.ID)
			continue
		}
		tier, ok := in.reverifyTier([]byte(d.Source), d.Signature, d.Tier, "drop", d.ID)
		if !ok {
			continue
		}
		if _, _, err := in.drops.AddRuntime(d.Name, d.Source, true); err != nil {
			errs = append(errs, fmt.Errorf("re-add drop %q: %w", d.Name, err))
			continue
		}
		in.mu.Lock()
		in.dropStates[d.ID] = dropState{Tier: tier, Provenance: d.Provenance}
		in.mu.Unlock()
		restored = append(restored, "drop:"+d.ID)
	}
	return restored, errs
}

// integrationManifestFile is the conventional path of an integration package's
// manifest within its repo.
const integrationManifestFile = "integration.json"

// InstallIntegrationFromGit fetches an integration package at repo@ref, reads
// its integration.json (+ optional integration.json.sig), and installs it with
// the operator-supplied credentials. The signature is verified over the EXACT
// file bytes (no JSON round-trip), so the trust tier is sound.
func (in *Installer) InstallIntegrationFromGit(ctx context.Context, repoURL, ref string, creds map[string]string) (IntegrationManifest, TrustTier, error) {
	fetched, err := (GitSource{}).Fetch(ctx, repoURL, ref)
	if err != nil {
		return IntegrationManifest{}, "", err
	}
	manifestBytes, err := fetched.File(integrationManifestFile)
	if err != nil {
		return IntegrationManifest{}, "", err
	}
	sig, err := fetched.Signature(integrationManifestFile)
	if err != nil {
		return IntegrationManifest{}, "", err
	}
	prov := Provenance{Repo: repoURL, Ref: ref, Commit: fetched.Commit}
	return in.InstallIntegration(ctx, manifestBytes, creds, sig, prov)
}

// InstallDropFromGit fetches a drop source at repo@ref:path (+ optional
// path.sig) and installs it (gated on its integration prerequisites).
func (in *Installer) InstallDropFromGit(ctx context.Context, repoURL, ref, path string) (core.Manifest, TrustTier, error) {
	fetched, err := (GitSource{}).Fetch(ctx, repoURL, ref)
	if err != nil {
		return core.Manifest{}, "", err
	}
	source, err := fetched.File(path)
	if err != nil {
		return core.Manifest{}, "", err
	}
	sig, err := fetched.Signature(path)
	if err != nil {
		return core.Manifest{}, "", err
	}
	prov := Provenance{Repo: repoURL, Ref: ref, Commit: fetched.Commit}
	return in.InstallDrop(ctx, path, string(source), sig, prov)
}

// UninstallIntegration removes an integration: unregisters its provider, clears
// its stored credentials, and removes the install record. Refused if an
// installed drop still requires it (symmetric with the install gate) — uninstall
// the dependent drops first.
func (in *Installer) UninstallIntegration(ctx context.Context, id string) error {
	if deps := in.dropsRequiring(id); len(deps) > 0 {
		return fmt.Errorf("integration %q is required by installed drop(s): %s", id, strings.Join(deps, ", "))
	}
	// Commit the durable removal first: if persistence fails we keep the
	// integration fully installed (consistent) rather than tearing down the
	// live provider only to have Restore bring it back on the next boot.
	if in.secrets != nil {
		if err := removeInstalledIntegration(ctx, in.secrets, id); err != nil {
			return fmt.Errorf("remove integration record %q: %w", id, err)
		}
		// Record gone == uninstalled; clean up creds best-effort (an orphan
		// here is dead weight, not a restore hazard).
		_ = deleteProviderCreds(ctx, in.secrets, id)
	}
	in.mu.Lock()
	delete(in.integrations, id)
	in.mu.Unlock()
	if in.oauth != nil {
		in.oauth.Unregister(id)
	}
	return nil
}

// UninstallDrop removes a drop (every installed version) from the catalog and
// its install record. The durable record is removed first so a persistence
// failure leaves the drop fully installed rather than dropping it from the live
// catalog only to have Restore re-add it on the next boot.
func (in *Installer) UninstallDrop(ctx context.Context, id string) error {
	if in.secrets != nil {
		if err := removeInstalledDrop(ctx, in.secrets, id); err != nil {
			return fmt.Errorf("remove drop record %q: %w", id, err)
		}
	}
	in.drops.Remove(id)
	in.mu.Lock()
	delete(in.dropStates, id)
	in.mu.Unlock()
	return nil
}

// dropsRequiring returns the "id@version" refs of installed drops that depend
// on the given oauth integration. It scans every installed version, not just
// the latest: an older pinned version can require an integration the latest
// one dropped, and that pinned flow would break if the integration were
// uninstalled out from under it. Refs are sorted for a stable error message.
func (in *Installer) dropsRequiring(integrationID string) []string {
	var deps []string
	for ref, man := range in.drops.PinnedManifestsForTenant("") {
		for _, req := range man.RequiresConnections {
			if req.Kind == "oauth" && req.Name == integrationID {
				deps = append(deps, ref)
				break
			}
		}
	}
	sort.Strings(deps)
	return deps
}

// IntegrationInstalled reports whether an integration is installed (the
// prerequisite gate primitive).
func (in *Installer) IntegrationInstalled(id string) bool {
	in.mu.RLock()
	defer in.mu.RUnlock()
	_, ok := in.integrations[id]
	return ok
}

// InstalledIntegrations lists installed integrations with their tiers.
func (in *Installer) InstalledIntegrations() []InstalledIntegration {
	in.mu.RLock()
	defer in.mu.RUnlock()
	out := make([]InstalledIntegration, 0, len(in.integrations))
	for id, st := range in.integrations {
		out = append(out, InstalledIntegration{ID: id, Version: st.Version, Tier: st.Tier, Provenance: st.Provenance})
	}
	return out
}

// InstalledDrops lists installed drops with their tiers and manifests.
func (in *Installer) InstalledDrops() []InstalledDropInfo {
	mans := in.drops.Manifests()
	in.mu.RLock()
	defer in.mu.RUnlock()
	out := make([]InstalledDropInfo, 0, len(mans))
	for id, man := range mans {
		st := in.dropStates[id]
		out = append(out, InstalledDropInfo{ID: id, Tier: st.Tier, Provenance: st.Provenance, Manifest: man})
	}
	return out
}
