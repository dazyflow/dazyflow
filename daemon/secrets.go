package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"git.sr.ht/~klahr/hazyflow/core"
)

// BuiltinProvider holds an in-memory map of name → value. Production
// deployments load it from an encrypted file at startup; the contents
// never leave the daemon's memory (modules see resolved values, not
// references, in their Job).
//
// Tenant isolation: Namespaced=true
// requires `<tenant>.<key>` naming and per-tenant access control.
// When set, the JSON file should be keyed by `<tenant>.<key>`
// strings; entries without a tenant prefix are unreachable from
// graphs.
type BuiltinProvider struct {
	mu         sync.RWMutex
	secrets    map[string]string
	Namespaced bool
}

func NewBuiltinProvider() *BuiltinProvider {
	return &BuiltinProvider{secrets: make(map[string]string)}
}

// NewBuiltinProviderFromFile reads a JSON map of name → value. The file
// format is the simplest thing that works:
//
//	{"stripe.api-key": "sk_live_...", "smtp.password": "..."}
//
// For production use, the file should be encrypted at rest (sealed
// secrets, age, vault-cli output, etc.); this loader doesn't do
// decryption itself.
func NewBuiltinProviderFromFile(path string) (*BuiltinProvider, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("builtin secrets file %q: %w", path, err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	p := NewBuiltinProvider()
	for k, v := range m {
		p.Set(k, v)
	}
	return p, nil
}

func (b *BuiltinProvider) Scheme() string { return "builtin" }

func (b *BuiltinProvider) Get(ctx context.Context, name string) (string, error) {
	if b.Namespaced {
		resolved, err := scopedName(ctx, name, "builtin")
		if err != nil {
			return "", err
		}
		name = resolved
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	v, ok := b.secrets[name]
	if !ok {
		return "", fmt.Errorf("builtin secret %q not found", name)
	}
	return v, nil
}

// scopedName validates that `name` is of the form `<tenant>.<key>`
// where tenant matches the caller's tenant from context, and returns
// the full string (with the prefix intact) for the downstream lookup.
// Used by builtin:// when Namespaced=true to enforce
// per-tenant ACL on shared secret stores.
//
// Returns a clear error in three cases:
//   - no tenant in context (the resolver was called outside a job —
//     tenant-scoped reads only make sense with a tenant identity)
//   - name doesn't contain a dot (no tenant prefix to verify)
//   - tenant prefix doesn't match the caller's tenant (cross-tenant
//     access attempt — the actual security check)
func scopedName(ctx context.Context, name, scheme string) (string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return "", fmt.Errorf("%s://%s: no tenant in context — tenant-isolated secrets need a tenant identity", scheme, name)
	}
	dot := -1
	for i := 0; i < len(name); i++ {
		if name[i] == '.' {
			dot = i
			break
		}
	}
	if dot <= 0 {
		return "", fmt.Errorf("%s://%s: tenant-isolated mode requires a `<tenant>.<key>` name (no prefix found)", scheme, name)
	}
	if name[:dot] != tenant {
		return "", fmt.Errorf("%s://%s: tenant prefix does not match this run's tenant", scheme, name)
	}
	return name, nil
}

// Set lets tests (and admin paths) populate the provider without going
// through the file loader.
func (b *BuiltinProvider) Set(name, value string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.secrets[name] = value
}

// Compile-time interface checks.
var _ core.SecretProvider = (*BuiltinProvider)(nil)
