// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package webapi

import (
	"sort"
	"sync"

	"git.sr.ht/~klahr/dazyflow/core"
)

// opKey scopes an operation's step id to its owning tenant.
//
// Keyed rather than filtered on read, for the same reason engine/mcp's toolKey
// is: a filter is a check someone can forget to write, a key is one the map
// cannot skip. By the time the engine hands a Job to a transport its params
// carry RESOLVED secrets — here, the tenant's own API credential — so a lookup
// that could cross tenants is a lookup that could send one org's credential to
// another org's service.
type opKey struct {
	tenant string
	id     string
}

type catalogKey struct {
	tenant string
	name   string
}

// Catalog is the registry of web-API steps the engine's NodeResolver queries.
//
// Unlike engine/mcp.Catalog it holds ONE population, not two: every catalog
// belongs to a tenant. There is no operator-configured instance-wide
// equivalent, because there is nothing to configure centrally — a described
// HTTP API is a tenant's own service, and an operator wanting one for everybody
// can write a native drop.
//
// Also unlike engine/mcp.Catalog, registration performs NO I/O. A descriptor is
// a document, so there is no handshake to run and nothing to keep alive: no
// connections, no goroutines, and therefore no Close. That is the same property
// that makes "is it working?" a genuinely different question here — see the
// design note; there is no LastConnected to report, only what a run found out.
type Catalog struct {
	mu       sync.RWMutex
	catalogs map[catalogKey]Descriptor
	ops      map[opKey]*Transport
}

func NewCatalog() *Catalog {
	return &Catalog{
		catalogs: make(map[catalogKey]Descriptor),
		ops:      make(map[opKey]*Transport),
	}
}

// Register validates a descriptor and files a transport per operation.
//
// Re-registering the same (tenant, name) REPLACES it, operations and all. That
// is what editing a catalog does — a changed base URL, a re-imported spec — and
// it has to take effect without the org first deleting the catalog and losing
// the steps its flows reference by id.
//
// The whole descriptor is validated before anything is filed, so one bad
// operation refuses the import instead of half-registering it.
func (c *Catalog) Register(desc Descriptor) error {
	if err := desc.Validate(); err != nil {
		return err
	}
	transports := make(map[opKey]*Transport, len(desc.Operations))
	for _, op := range desc.Operations {
		transports[opKey{tenant: desc.Tenant, id: StepID(desc.Name, op.ID)}] = &Transport{
			desc:     desc,
			op:       op,
			manifest: synthesizeManifest(desc, op),
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	key := catalogKey{tenant: desc.Tenant, name: desc.Name}
	if _, exists := c.catalogs[key]; exists {
		c.detachLocked(key)
	}
	c.catalogs[key] = desc
	for k, t := range transports {
		c.ops[k] = t
	}
	return nil
}

// Unregister drops a catalog and its steps. An unknown pair is not an error:
// deleting a catalog that failed to register in the first place is the normal
// way an org clears up a mistake, and reporting "not found" would leave a row
// nobody can remove.
func (c *Catalog) Unregister(tenant, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.detachLocked(catalogKey{tenant: tenant, name: name})
}

// detachLocked removes a catalog and its operations. The caller holds c.mu.
func (c *Catalog) detachLocked(key catalogKey) {
	delete(c.catalogs, key)
	for id, t := range c.ops {
		if id.tenant == key.tenant && t.desc.Name == key.name {
			delete(c.ops, id)
		}
	}
}

// Get returns the transport for id as seen BY tenant, and nothing outside it.
//
// An empty tenant matches nothing. That is the honest answer for a caller with
// no tenant (docs generation, a background task): every catalog here is some
// org's, so there is no instance-wide population to fall back to.
func (c *Catalog) Get(tenant, id string) (core.Transport, bool) {
	if tenant == "" {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.ops[opKey{tenant: tenant, id: id}]
	if !ok {
		return nil, false
	}
	return t, true
}

// ManifestsFor returns every web-API manifest visible to tenant.
func (c *Catalog) ManifestsFor(tenant string) map[string]core.Manifest {
	if tenant == "" {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]core.Manifest)
	for id, t := range c.ops {
		if id.tenant == tenant {
			out[id.id] = t.manifest
		}
	}
	return out
}

// AllManifests returns every web-API manifest on the instance, with the tenants
// that can resolve each id.
//
// The one legitimate caller is the platform killswitch page, which is
// instance-wide by definition: a platform admin has to be able to switch off a
// misbehaving org's catalog. Nothing that ROUTES may use this — it flattens
// tenants, which is exactly the confusion opKey exists to prevent.
func (c *Catalog) AllManifests() (map[string]core.Manifest, map[string][]string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	manifests := make(map[string]core.Manifest, len(c.ops))
	tenants := map[string][]string{}
	for id, t := range c.ops {
		manifests[id.id] = t.manifest
		tenants[id.id] = append(tenants[id.id], id.tenant)
	}
	for id := range tenants {
		sort.Strings(tenants[id])
	}
	return manifests, tenants
}

// CatalogStatus is what one registered catalog looks like to an admin page.
type CatalogStatus struct {
	Name    string
	Tenant  string
	BaseURL string
	StepIDs []string
}

// CatalogsFor lists a tenant's catalogs, sorted by name so a polled list does
// not reshuffle between refreshes.
func (c *Catalog) CatalogsFor(tenant string) []CatalogStatus {
	if tenant == "" {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []CatalogStatus
	for key, desc := range c.catalogs {
		if key.tenant != tenant {
			continue
		}
		out = append(out, CatalogStatus{
			Name:    desc.Name,
			Tenant:  desc.Tenant,
			BaseURL: desc.BaseURL,
			StepIDs: desc.operationIDs(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
