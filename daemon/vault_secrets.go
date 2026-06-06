package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	openbao "github.com/openbao/openbao/api/v2"
)

// VaultProvider is the "bring your own secret manager" SecretProvider: it
// resolves ${vault.PATH#FIELD} against each TENANT's own OpenBao / HashiCorp
// Vault, rather than the built-in encrypted store. An org that already runs a
// secret manager points the platform at it (per-tenant config) and references
// its secrets in flows; an org that doesn't keeps using the built-in encrypted
// secret store. The two coexist — this is additive, not a replacement.
//
// Vault and OpenBao share the same HTTP API, so one provider serves both; the
// scheme is "vault" (the conventional name, also used in the SecretProvider
// docs). Reads are cached per (tenant, path, field) for a short TTL so a flow
// referencing a secret on every run doesn't hammer the manager.
type VaultProvider struct {
	client vaultClient
	// loadConfig returns the calling tenant's connection config. ok=false means
	// the tenant hasn't configured a secret manager (a clear "not configured"
	// error, not a failure). Backed by the encrypted store in production.
	loadConfig func(ctx context.Context, tenant string) (cfg VaultConfig, ok bool, err error)
	ttl        time.Duration

	mu    sync.Mutex
	cache map[string]vaultCacheEntry
}

type vaultCacheEntry struct {
	value string
	exp   time.Time
}

// defaultVaultCacheTTL bounds how stale a resolved value can be. Short enough
// that a rotated upstream secret is picked up quickly, long enough that a busy
// flow doesn't round-trip the manager every run.
const defaultVaultCacheTTL = 60 * time.Second

// NewVaultProvider builds the provider. ttl <= 0 uses defaultVaultCacheTTL.
func NewVaultProvider(client vaultClient, loadConfig func(context.Context, string) (VaultConfig, bool, error), ttl time.Duration) *VaultProvider {
	if ttl <= 0 {
		ttl = defaultVaultCacheTTL
	}
	return &VaultProvider{client: client, loadConfig: loadConfig, ttl: ttl, cache: map[string]vaultCacheEntry{}}
}

// NewVaultProviderForStore builds the production provider: a real OpenBao/Vault
// HTTP client, with each tenant's config loaded from the encrypted store.
func NewVaultProviderForStore(es *EncryptedSecrets, httpTimeout time.Duration) *VaultProvider {
	return NewVaultProvider(
		newVaultAPIClient(httpTimeout),
		func(ctx context.Context, tenant string) (VaultConfig, bool, error) {
			return loadVaultConfig(ctx, es, tenant)
		},
		0,
	)
}

// VerifyVaultConfig validates a config and checks it connects + authenticates,
// using a one-off client. Used by the "save secret-manager config" endpoint to
// catch a bad address/credential before persisting it.
func VerifyVaultConfig(ctx context.Context, cfg VaultConfig, timeout time.Duration) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	return newVaultAPIClient(timeout).verify(ctx, cfg)
}

func (p *VaultProvider) Scheme() string { return "vault" }

// Get resolves "PATH#FIELD": reads the KV-v2 secret at PATH from the tenant's
// configured manager and returns FIELD. The tenant comes from context — a
// BYO secret is always tenant-scoped, never global.
func (p *VaultProvider) Get(ctx context.Context, ref string) (string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("vault://%s: no tenant in context — BYO secrets are tenant-scoped", ref)
	}
	path, field, err := splitVaultRef(ref)
	if err != nil {
		return "", err
	}
	key := tenant + "\x00" + path + "\x00" + field
	if v, ok := p.cached(key); ok {
		return v, nil
	}

	cfg, ok, err := p.loadConfig(ctx, tenant)
	if err != nil {
		return "", fmt.Errorf("vault: loading this tenant's secret-manager config: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("vault://%s: this tenant has no secret manager configured", ref)
	}
	fields, err := p.client.readKV(ctx, cfg, path)
	if err != nil {
		return "", fmt.Errorf("vault: reading %q from %s: %w", path, cfg.Address, err)
	}
	val, ok := fields[field]
	if !ok {
		return "", fmt.Errorf("vault: secret %q has no field %q", path, field)
	}
	p.store(key, val)
	return val, nil
}

func (p *VaultProvider) cached(key string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.cache[key]
	if !ok || !e.exp.After(nowFunc()) {
		return "", false
	}
	return e.value, true
}

func (p *VaultProvider) store(key, val string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cache[key] = vaultCacheEntry{value: val, exp: nowFunc().Add(p.ttl)}
}

// nowFunc is a clock seam for the cache-expiry tests.
var nowFunc = time.Now

// splitVaultRef parses "PATH#FIELD". The field is required: a KV-v2 secret is a
// map of fields, so a reference has to say which one (no guessing).
func splitVaultRef(ref string) (path, field string, err error) {
	i := strings.LastIndexByte(ref, '#')
	if i <= 0 || i == len(ref)-1 {
		return "", "", fmt.Errorf("vault reference %q must be PATH#FIELD (e.g. stripe#api_key)", ref)
	}
	return ref[:i], ref[i+1:], nil
}

// VaultConfig is one tenant's connection to its OpenBao / Vault. It carries auth
// credentials, so it is itself a secret — stored encrypted in the tenant's own
// built-in store, never in plaintext config.
type VaultConfig struct {
	Address   string    `json:"address"`             // e.g. https://openbao.internal:8200
	Namespace string    `json:"namespace,omitempty"` // OpenBao/Vault Enterprise namespace
	Mount     string    `json:"mount"`               // KV-v2 mount, e.g. "secret"
	Auth      VaultAuth `json:"auth"`
}

// VaultAuth selects how the daemon authenticates to the tenant's manager.
type VaultAuth struct {
	Method   string `json:"method"`              // "token" or "approle"
	Token    string `json:"token,omitempty"`     // method=token: a (long-lived) token
	RoleID   string `json:"role_id,omitempty"`   // method=approle
	SecretID string `json:"secret_id,omitempty"` // method=approle
}

func (c VaultConfig) validate() error {
	if c.Address == "" {
		return fmt.Errorf("address is required")
	}
	if !strings.HasPrefix(c.Address, "https://") && !strings.HasPrefix(c.Address, "http://") {
		return fmt.Errorf("address must be an http(s) URL")
	}
	if c.Mount == "" {
		return fmt.Errorf("mount is required (the KV-v2 mount, e.g. \"secret\")")
	}
	switch c.Auth.Method {
	case "token":
		if c.Auth.Token == "" {
			return fmt.Errorf("auth.token is required for method=token")
		}
	case "approle":
		if c.Auth.RoleID == "" || c.Auth.SecretID == "" {
			return fmt.Errorf("auth.role_id and auth.secret_id are required for method=approle")
		}
	default:
		return fmt.Errorf("auth.method must be \"token\" or \"approle\"")
	}
	return nil
}

// vaultClient is the minimal OpenBao/Vault surface the provider needs: read a
// KV-v2 secret's fields, and verify connectivity+auth (used when an admin saves
// a config). Split out so tests run without a live server.
type vaultClient interface {
	readKV(ctx context.Context, cfg VaultConfig, path string) (map[string]string, error)
	// verify checks the address is reachable and the credentials authenticate,
	// without needing a specific secret path.
	verify(ctx context.Context, cfg VaultConfig) error
}

// vaultConfigSecretName is the reserved name under which a tenant's secret-
// manager config is stored in its own encrypted store. The "cfg:" prefix is
// filtered out of the user-facing secret listing (see filterReservedSecretNames).
const vaultConfigSecretName = "cfg:secret-manager"

// saveVaultConfig validates and persists a tenant's secret-manager config
// (encrypted, in the tenant's own store).
func saveVaultConfig(ctx context.Context, es *EncryptedSecrets, tenant string, cfg VaultConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return es.Put(ctx, tenant, vaultConfigSecretName, string(b))
}

// loadVaultConfig returns a tenant's config; ok=false when none is set.
func loadVaultConfig(ctx context.Context, es *EncryptedSecrets, tenant string) (VaultConfig, bool, error) {
	raw, err := es.Get(core.WithTenant(ctx, tenant), vaultConfigSecretName)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return VaultConfig{}, false, nil
		}
		return VaultConfig{}, false, err
	}
	var cfg VaultConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return VaultConfig{}, false, fmt.Errorf("decode secret-manager config: %w", err)
	}
	return cfg, true, nil
}

func deleteVaultConfig(ctx context.Context, es *EncryptedSecrets, tenant string) error {
	return es.Delete(ctx, tenant, vaultConfigSecretName)
}

// filterReservedSecretNames drops internal "cfg:" entries (e.g. the secret-
// manager config) from a user-facing secret listing.
func filterReservedSecretNames(names []string) []string {
	out := names[:0:0]
	for _, n := range names {
		if strings.HasPrefix(n, "cfg:") {
			continue
		}
		out = append(out, n)
	}
	return out
}

// vaultAPIClient talks to OpenBao/Vault through OpenBao's official Go SDK
// (github.com/openbao/openbao/api/v2) — so token handling, KV-v2 path munging,
// namespaces, TLS, and retries are the SDK's job, not ours. It deliberately does
// NOT use the flow-egress SSRF guard: a secret manager normally lives at an
// internal/private address, and the address is admin-configured per tenant, not
// attacker-controlled flow input. AppRole-derived tokens are cached until
// shortly before their lease expires.
type vaultAPIClient struct {
	timeout time.Duration

	mu     sync.Mutex
	tokens map[string]appRoleToken // key: address|role_id
}

type appRoleToken struct {
	token string
	exp   time.Time
}

func newVaultAPIClient(timeout time.Duration) *vaultAPIClient {
	return &vaultAPIClient{timeout: timeout, tokens: map[string]appRoleToken{}}
}

// newClient builds an unauthenticated SDK client for cfg's address/namespace.
func (c *vaultAPIClient) newClient(cfg VaultConfig) (*openbao.Client, error) {
	conf := openbao.DefaultConfig()
	conf.Address = cfg.Address
	if c.timeout > 0 {
		conf.Timeout = c.timeout
	}
	cl, err := openbao.NewClient(conf)
	if err != nil {
		return nil, err
	}
	if cfg.Namespace != "" {
		cl.SetNamespace(cfg.Namespace)
	}
	return cl, nil
}

func (c *vaultAPIClient) authedClient(ctx context.Context, cfg VaultConfig) (*openbao.Client, error) {
	cl, err := c.newClient(cfg)
	if err != nil {
		return nil, err
	}
	tok, err := c.token(ctx, cfg)
	if err != nil {
		return nil, err
	}
	cl.SetToken(tok)
	return cl, nil
}

func (c *vaultAPIClient) readKV(ctx context.Context, cfg VaultConfig, path string) (map[string]string, error) {
	cl, err := c.authedClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	sec, err := cl.KVv2(cfg.Mount).Get(ctx, path)
	if err != nil {
		return nil, err
	}
	if sec == nil || sec.Data == nil {
		return nil, fmt.Errorf("secret %q has no data", path)
	}
	out := make(map[string]string, len(sec.Data))
	for k, v := range sec.Data {
		out[k] = stringifyVaultValue(v)
	}
	return out, nil
}

// verify confirms the address is reachable and the credentials authenticate:
// an AppRole login, or a token self-lookup. It reads no specific secret.
func (c *vaultAPIClient) verify(ctx context.Context, cfg VaultConfig) error {
	if cfg.Auth.Method == "approle" {
		_, _, err := c.appRoleLogin(ctx, cfg)
		return err
	}
	cl, err := c.newClient(cfg)
	if err != nil {
		return err
	}
	cl.SetToken(cfg.Auth.Token)
	_, err = cl.Logical().ReadWithContext(ctx, "auth/token/lookup-self")
	return err
}

// token returns a usable client token for cfg: the static token as-is, or an
// AppRole login result (cached until near lease expiry).
func (c *vaultAPIClient) token(ctx context.Context, cfg VaultConfig) (string, error) {
	if cfg.Auth.Method == "token" {
		return cfg.Auth.Token, nil
	}
	key := cfg.Address + "|" + cfg.Auth.RoleID
	c.mu.Lock()
	if t, ok := c.tokens[key]; ok && t.exp.After(time.Now()) {
		c.mu.Unlock()
		return t.token, nil
	}
	c.mu.Unlock()

	tok, ttl, err := c.appRoleLogin(ctx, cfg)
	if err != nil {
		return "", err
	}
	// Renew a little before the lease actually ends.
	lifetime := ttl - 10*time.Second
	if lifetime < 0 {
		lifetime = 0
	}
	c.mu.Lock()
	c.tokens[key] = appRoleToken{token: tok, exp: time.Now().Add(lifetime)}
	c.mu.Unlock()
	return tok, nil
}

func (c *vaultAPIClient) appRoleLogin(ctx context.Context, cfg VaultConfig) (token string, ttl time.Duration, err error) {
	cl, err := c.newClient(cfg)
	if err != nil {
		return "", 0, err
	}
	sec, err := cl.Logical().WriteWithContext(ctx, "auth/approle/login", map[string]any{
		"role_id":   cfg.Auth.RoleID,
		"secret_id": cfg.Auth.SecretID,
	})
	if err != nil {
		return "", 0, err
	}
	if sec == nil || sec.Auth == nil || sec.Auth.ClientToken == "" {
		return "", 0, fmt.Errorf("approle login returned no client_token")
	}
	return sec.Auth.ClientToken, time.Duration(sec.Auth.LeaseDuration) * time.Second, nil
}

// stringifyVaultValue renders a KV field value as a string: strings pass
// through; everything else is JSON-encoded so a structured value still resolves
// to something usable rather than a Go-fmt blob.
func stringifyVaultValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

var _ core.SecretProvider = (*VaultProvider)(nil)
