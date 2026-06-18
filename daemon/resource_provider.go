package daemon

import (
	"context"
	"encoding/json"
	"fmt"

	"git.sr.ht/~klahr/dazyflow/core"
)

// ResourceFetcher fetches the live content of one resource definition —
// e.g. for type "google_sheet", the sheet's rows + headers. Registered
// per type in cmd/dzd so the daemon stays free of drop/connector imports
// (the same looseness wireConnectorTokenHooks uses). tenant/flow ride on
// ctx, so a fetcher can resolve the right OAuth account.
type ResourceFetcher func(ctx context.Context, def core.ResourceDef) (any, error)

// ResourceProvider implements core.ResourceProvider (the "resource"
// reference scheme). It loads a flow/org-scoped ResourceDef from the
// encrypted secret store (under the reserved "res." namespace, with the
// same flow→organization cascade secrets use) and dispatches to the
// type's registered fetcher. The fetched value is returned structured so
// ${resource.NAME.rows} hands a real array to the node.
type ResourceProvider struct {
	Secrets  *EncryptedSecrets
	Fetchers map[string]ResourceFetcher
}

func (p *ResourceProvider) Scheme() string { return "resource" }

func (p *ResourceProvider) Resolve(ctx context.Context, name string) (any, error) {
	def, err := p.loadDef(ctx, name)
	if err != nil {
		return nil, err
	}
	fetch, ok := p.Fetchers[def.Type]
	if !ok {
		return nil, fmt.Errorf("resource %q: unsupported type %q", name, def.Type)
	}
	return fetch(ctx, def)
}

// loadDef reads NAME's definition. Get applies the flow→organization
// cascade: a flow's own resource of that name wins over an org-wide one,
// keyed off the flow on ctx (set by the engine before resolution).
func (p *ResourceProvider) loadDef(ctx context.Context, name string) (core.ResourceDef, error) {
	if p.Secrets == nil {
		return core.ResourceDef{}, fmt.Errorf("resource store not configured")
	}
	raw, err := p.Secrets.Get(ctx, secretResourcePrefix+name)
	if err != nil {
		return core.ResourceDef{}, fmt.Errorf("resource %q not found", name)
	}
	var def core.ResourceDef
	if err := json.Unmarshal([]byte(raw), &def); err != nil {
		return core.ResourceDef{}, fmt.Errorf("resource %q: corrupt definition", name)
	}
	if def.Name == "" {
		def.Name = name
	}
	return def, nil
}
