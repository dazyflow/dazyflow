package jsdrop

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// dropKey identifies an exact drop version. Scripted/marketplace drops are
// always pinned; the bare-id (built-in) world lives in the other catalogs.
type dropKey struct {
	ID      string
	Version string
}

// Catalog holds scripted (JS/TS) drops as the 5th NodeResolver catalog. It
// keeps every drop's manifest + transpiled ESM in a shared base and tracks, per
// tenant, which versions are installed — so tenant A's install is invisible to
// tenant B and a graph can pin an exact version that an install-update won't
// disturb.
//
// There is NO in-process JS engine here: a drop's manifest is read by the Node
// runtime (the Extract hook, `drophost.mjs --emit-manifest`) and a drop is
// executed by the Node runtime (the Run hook, broker + runner). Both are wired
// by the daemon; the catalog stays free of any containerdrop/Node dependency.
//
// The tenant-aware methods (Add*/Install/GetForTenant/ManifestsForTenant) are
// the real API the NodeResolver drives off the executing tenant; Get/Manifests
// remain as thin global-default ("" tenant) shims for tenant-agnostic callers.
type Catalog struct {
	mu sync.RWMutex
	// base holds every drop version's manifest, shared across tenants — stored
	// once on Add and reused by everyone who installs it.
	base map[dropKey]core.Manifest
	// installed records which versions each tenant has. The "" tenant is the
	// global default set (official / boot-loaded drops every tenant sees
	// unless removed), so a tenant's effective set is
	// installed[tenant] ∪ installed[""].
	installed map[string]map[dropKey]struct{}
	// esm holds each version transpiled to a single ESM module (TS→JS), the form
	// the out-of-process Node drop host imports.
	esm map[dropKey]string
	// trusted marks versions whose code we vouch for (official embedded drops,
	// or marketplace installs signed by an official/verified key). Untrusted
	// versions (community / runtime-extracted) get a stricter egress default:
	// an empty allowlist denies all fetch rather than falling back to the
	// process-wide policy. See the Run hook wiring in cmd/hzd/sandbox.go.
	trusted map[dropKey]bool

	// Run builds the Transport that executes a drop — the daemon wires it to the
	// out-of-process Node host (broker + runner). It receives the transpiled ESM
	// module and whether the drop is trusted (official/verified vs community),
	// which the daemon uses to pick the egress default. Typed in core terms so
	// the catalog stays free of any containerdrop dependency. Nil → drops can be
	// cataloged and inspected but NOT executed (GetForTenant returns false); the
	// daemon always wires it.
	Run func(manifest core.Manifest, jsESM string, trusted bool) core.Transport

	// Extract reads a drop's manifest from its source by running the same Node
	// runtime that will execute it (`drophost.mjs --emit-manifest`). The daemon
	// wires it; AddRuntime/Inspect/Register/LoadDir require it. Official drops use
	// AddPrebuilt with a generate-time-embedded manifest and don't need it.
	Extract func(name, source string) (core.Manifest, error)
}

func NewCatalog() *Catalog {
	return &Catalog{
		base:      make(map[dropKey]core.Manifest),
		installed: make(map[string]map[dropKey]struct{}),
		esm:       make(map[dropKey]string),
		trusted:   make(map[dropKey]bool),
	}
}

// AddPrebuilt stores a drop whose manifest is already known — the official-drop
// path, where the manifest is embedded at generate time so boot needs no Node
// spawn. It enforces the registration minimums native drops get (id, summary,
// ≥1 example) plus a version (required for marketplace install/pinning),
// transpiles the source to ESM for the Node host, and keys it by (id, version).
// When global is true the version also joins the default set every tenant sees.
// trusted marks the drop as official/verified (vs community) — it governs the
// egress default the Run hook applies. Returns the resolved id+version.
func (c *Catalog) AddPrebuilt(name, source string, man core.Manifest, global, trusted bool) (id, version string, err error) {
	if man.ID == "" {
		return "", "", fmt.Errorf("scripted drop %q: manifest.id is required", name)
	}
	if man.Summary == "" {
		return "", "", fmt.Errorf("scripted drop %q: manifest.summary is required", man.ID)
	}
	if man.Version == "" {
		return "", "", fmt.Errorf("scripted drop %q: manifest.version is required for marketplace install", man.ID)
	}
	if len(man.Examples) == 0 {
		return "", "", fmt.Errorf("scripted drop %q: manifest.examples must have at least one entry", man.ID)
	}
	esmJS, err := TranspileESM(source)
	if err != nil {
		return "", "", fmt.Errorf("drop %q: %w", man.ID, err)
	}
	key := dropKey{man.ID, man.Version}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.base[key]; exists {
		return "", "", fmt.Errorf("scripted drop %s@%s already added", key.ID, key.Version)
	}
	c.base[key] = man
	c.esm[key] = esmJS
	c.trusted[key] = trusted
	if global {
		c.installLocked("", key)
	}
	return key.ID, key.Version, nil
}

// AddRuntime stores a drop whose manifest must be extracted from source — the
// runtime-install path (marketplace / boot Restore / LoadDir). It reads the
// manifest via the Node Extract hook, then defers to AddPrebuilt. Requires the
// daemon to have wired Extract. Runtime-extracted drops are never trusted —
// they're community code unless an explicit AddPrebuilt(trusted=true) from a
// signature-verified install path says otherwise.
func (c *Catalog) AddRuntime(name, source string, global bool) (id, version string, err error) {
	man, err := c.Inspect(name, source)
	if err != nil {
		return "", "", err
	}
	return c.AddPrebuilt(name, source, man, global, false)
}

// Inspect reads a drop's core.Manifest from source WITHOUT adding it to the
// catalog — for pre-install checks like dependency gating or capability
// preview. Routes through the Node Extract hook (no in-process JS engine).
func (c *Catalog) Inspect(name, source string) (core.Manifest, error) {
	if c.Extract == nil {
		return core.Manifest{}, fmt.Errorf("scripted-drop manifest extraction is not configured (Node runtime unavailable)")
	}
	return c.Extract(name, source)
}

// Install records that a tenant has a specific, already-Added drop version.
func (c *Catalog) Install(tenant, id, version string) error {
	if tenant == "" {
		return fmt.Errorf("install: tenant required (use Add(global=true) for the default set)")
	}
	key := dropKey{id, version}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.base[key]; !ok {
		return fmt.Errorf("install: %s@%s is not in the catalog", id, version)
	}
	c.installLocked(tenant, key)
	return nil
}

// Uninstall removes a version from a tenant's set. Idempotent; the version
// stays in the base for other tenants.
func (c *Catalog) Uninstall(tenant, id, version string) error {
	key := dropKey{id, version}
	c.mu.Lock()
	defer c.mu.Unlock()
	if set := c.installed[tenant]; set != nil {
		delete(set, key)
		if len(set) == 0 {
			delete(c.installed, tenant)
		}
	}
	return nil
}

// Remove fully removes a drop id — every version, from the base and from every
// tenant's install set. Used by the marketplace uninstall (which installs
// globally, so a per-(tenant,version) Uninstall isn't enough).
func (c *Catalog) Remove(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.base {
		if k.ID == id {
			delete(c.base, k)
			delete(c.esm, k)
			delete(c.trusted, k)
		}
	}
	for tenant, set := range c.installed {
		for k := range set {
			if k.ID == id {
				delete(set, k)
			}
		}
		if len(set) == 0 {
			delete(c.installed, tenant)
		}
	}
}

// GetForTenant resolves an exact pinned version visible to the tenant. An empty
// version means "latest installed for this tenant" (authoring convenience only
// — graphs always carry an explicit version for run-time determinism). A
// version present in the base but not installed for the tenant does NOT
// resolve.
func (c *Catalog) GetForTenant(tenant, id, version string) (core.Transport, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if version == "" {
		if version = c.latestVisibleLocked(tenant, id); version == "" {
			return nil, false
		}
	}
	key := dropKey{id, version}
	man, ok := c.base[key]
	if !ok {
		return nil, false
	}
	if !c.visibleLocked(tenant, key) {
		return nil, false
	}
	// Every drop runs out-of-process via the Run hook (the Node host). Without
	// it the catalog can list/inspect drops but not execute them.
	if c.Run == nil {
		return nil, false
	}
	return c.Run(man, c.esm[key], c.trusted[key]), true
}

// ManifestsForTenant returns one manifest per visible drop id (latest installed
// version) — the tenant's palette / ListDrops view.
func (c *Catalog) ManifestsForTenant(tenant string) map[string]core.Manifest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	latest := map[string]string{} // id -> best visible version
	consider := func(set map[dropKey]struct{}) {
		for k := range set {
			if cur, ok := latest[k.ID]; !ok || versionLess(cur, k.Version) {
				latest[k.ID] = k.Version
			}
		}
	}
	consider(c.installed[tenant])
	consider(c.installed[""])
	out := make(map[string]core.Manifest, len(latest))
	for id, v := range latest {
		if man, ok := c.base[dropKey{id, v}]; ok {
			out[id] = man
		}
	}
	return out
}

// PinnedManifestsForTenant returns a manifest for every exact version visible
// to the tenant, keyed by "id@version". The NodeResolver folds these into the
// validation manifest set so a graph node that pins "gmail_send_email@2.0.0"
// resolves during ValidateWithManifests — the bare-id entry from
// ManifestsForTenant only covers the latest version.
func (c *Catalog) PinnedManifestsForTenant(tenant string) map[string]core.Manifest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := map[string]core.Manifest{}
	add := func(set map[dropKey]struct{}) {
		for k := range set {
			if man, ok := c.base[k]; ok {
				out[k.ID+"@"+k.Version] = man
			}
		}
	}
	add(c.installed[tenant])
	add(c.installed[""])
	return out
}

// Register extracts a drop's manifest and adds it as globally visible — the
// boot/LoadDir path for runtime drops on disk. Back-compat shim over
// AddRuntime(global=true); requires the Node Extract hook.
func (c *Catalog) Register(name, source string) error {
	_, _, err := c.AddRuntime(name, source, true)
	return err
}

// Get resolves the latest globally-default version of id. Global-default shim
// for tenant-agnostic callers; the resolver uses GetForTenant.
func (c *Catalog) Get(id string) (core.Transport, bool) {
	return c.GetForTenant("", id, "")
}

// Manifests returns the global-default palette. Shim for tenant-agnostic
// callers; the resolver uses ManifestsForTenant.
func (c *Catalog) Manifests() map[string]core.Manifest {
	return c.ManifestsForTenant("")
}

// LoadDir registers every *.ts / *.js file in dir as globally visible (mirrors
// LocalCatalog.LoadDir). Errors are joined so one broken drop doesn't block the
// rest. Requires the Node Extract hook.
func (c *Catalog) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read dir %q: %w", dir, err)
	}
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !(strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".js")) {
			continue
		}
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		if err := c.Register(name, string(src)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

// LoadFS registers every *.ts / *.js file at the root of fsys as globally
// visible — the embed.FS analog of LoadDir, for drops shipped inside the
// binary. Requires the Node Extract hook (official drops use AddPrebuilt with
// embedded manifests instead — see officialdrops). Errors are joined so one
// broken drop doesn't block the rest.
func (c *Catalog) LoadFS(fsys fs.FS) error {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return fmt.Errorf("read embedded drops: %w", err)
	}
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !(strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".js")) {
			continue
		}
		src, err := fs.ReadFile(fsys, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if err := c.Register(name, string(src)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// ── unexported helpers (callers hold the appropriate lock) ────────────────

func (c *Catalog) installLocked(tenant string, key dropKey) {
	set := c.installed[tenant]
	if set == nil {
		set = make(map[dropKey]struct{})
		c.installed[tenant] = set
	}
	set[key] = struct{}{}
}

func (c *Catalog) visibleLocked(tenant string, key dropKey) bool {
	if _, ok := c.installed[tenant][key]; ok {
		return true
	}
	_, ok := c.installed[""][key]
	return ok
}

// latestVisibleLocked returns the highest version of id visible to the tenant
// (their installs ∪ the global default), or "" if none is visible.
func (c *Catalog) latestVisibleLocked(tenant, id string) string {
	best := ""
	consider := func(set map[dropKey]struct{}) {
		for k := range set {
			if k.ID == id && (best == "" || versionLess(best, k.Version)) {
				best = k.Version
			}
		}
	}
	consider(c.installed[tenant])
	consider(c.installed[""])
	return best
}

// versionLess reports whether version a sorts before b. Dot-separated segments
// compare numerically when both are integers ("1.9" < "1.10"), else lexically.
// Best-effort — enough to pick a "latest" for the palette; exact pins, not this
// ordering, drive execution.
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv string
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		an, aerr := strconv.Atoi(av)
		bn, berr := strconv.Atoi(bv)
		if aerr == nil && berr == nil {
			if an != bn {
				return an < bn
			}
			continue
		}
		if av != bv {
			return av < bv
		}
	}
	return false
}
