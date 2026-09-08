// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package webapi

import (
	"sort"
	"sync"

	"github.com/dazyflow/dazyflow/core"
)

// opKey scopes a step id to its tenant by KEY rather than a read-time filter,
// as engine/mcp's toolKey does: a filter is a check someone can forget to write.
// A Job reaching a transport carries RESOLVED secrets — here the tenant's own API
// credential — so a lookup that could cross tenants could send one org's
// credential to another org's service.
type opKey struct {
	tenant string
	id     string
}

type catalogKey struct {
	tenant string
	name   string
}

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

// Register REPLACES an existing (tenant, name), operations and all: that is what
// editing a catalog does, and it has to take effect without the org first
// deleting the catalog and losing the steps its flows reference by id. The whole
// descriptor is validated before anything is filed, so one bad operation refuses
// the import instead of half-registering it.
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

// Unregister treats an unknown pair as success: deleting a catalog that failed
// to register is the normal way an org clears up a mistake, and "not found" would
// leave a row nobody can remove.
func (c *Catalog) Unregister(tenant, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.detachLocked(catalogKey{tenant: tenant, name: name})
}

func (c *Catalog) detachLocked(key catalogKey) {
	delete(c.catalogs, key)
	for id, t := range c.ops {
		if id.tenant == key.tenant && t.desc.Name == key.name {
			delete(c.ops, id)
		}
	}
}

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

type CatalogStatus struct {
	Name    string
	Tenant  string
	BaseURL string
	StepIDs []string
}

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
