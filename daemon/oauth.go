package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// OAuth 2.0 authorization-code flow. This is what makes the
// "connect your Slack account" experience work without the user
// pasting tokens by hand — the daemon shepherds them through the
// provider's authorization page, takes the code back, exchanges it
// for tokens, and stores those tokens in the encrypted secret store
// keyed by (tenant, provider, account_name).
//
// Flow:
//
//	1. User clicks "Connect Slack" in the UI.
//	2. Browser hits GET /api/v1/oauth/slack/authorize?return_to=...
//	   The handler mints a random state, parks the (tenant, provider,
//	   account_name, return_to) tuple under that state, and 302s the
//	   user to Slack's authorize URL.
//	3. User authorizes on Slack's site.
//	4. Slack 302s the user back to
//	   GET /api/v1/oauth/slack/callback?code=...&state=...
//	   (NB: this endpoint is UNAUTHENTICATED — the state token is the
//	   only thing tying the callback to the original user.)
//	5. Handler validates state, exchanges code for tokens via Slack's
//	   token endpoint, stores the result in EncryptedSecrets, and 302s
//	   the user to return_to.
//
// What this DELIBERATELY isn't (yet):
//
//   - PKCE: helpful for SPAs and mobile; not needed for the
//     server-side flow we have. Add if/when we expose OAuth from a
//     browser-only client.
//   - Per-tenant client_id/secret: enterprises sometimes want their
//     own OAuth app to control which scopes show in the consent
//     screen. T3 feature.
//   - Refresh-on-expiry: we store refresh_token but don't yet
//     transparently refresh in Get. Drops calling getOAuthToken get
//     whatever's stored. Refresh becomes important once we have
//     long-lived integrations; deferred.

// OAuthProvider describes how to talk to one OAuth 2.0 provider.
// Hardcoded per provider (Slack, Gmail, GitHub, etc.) because the
// URLs and scope shapes don't change per-deployment. ClientID and
// ClientSecret come from config (env vars, typically).
type OAuthProvider struct {
	Name         string   // e.g. "slack", "github" — matches the URL slug
	AuthorizeURL string   // provider's authorize endpoint
	TokenURL     string   // provider's code-exchange endpoint
	Scopes       []string // OAuth scopes to request; provider-specific shape
	ClientID     string   // OAuth client_id (provider-issued)
	ClientSecret string   // OAuth client_secret (keep out of logs)

	// AuthorizeExtras are provider-specific query parameters appended
	// to the authorize URL. The canonical example is Google's
	// `access_type=offline` + `prompt=consent`, required to get a
	// refresh_token (without them you get an access_token only,
	// good for one hour and then dead).
	AuthorizeExtras map[string]string
}

// OAuthRegistry holds the set of providers the daemon can drive,
// plus the in-process state store and the encrypted secrets backend
// it writes tokens into.
type OAuthRegistry struct {
	providers map[string]OAuthProvider
	state     *oauthStateStore
	secrets   *EncryptedSecrets

	// BaseURL is the externally-reachable origin of the daemon
	// (e.g. "https://app.example.com"). Used to build the
	// redirect_uri that goes to the OAuth provider; this MUST
	// match the URL the provider has registered for this client.
	BaseURL string

	// HTTPClient lets tests stub provider calls. nil = http.DefaultClient.
	HTTPClient *http.Client
}

// NewOAuthRegistry constructs a registry. baseURL is the
// externally-reachable origin (no trailing slash). secrets must be
// non-nil — that's where exchanged tokens land.
func NewOAuthRegistry(baseURL string, secrets *EncryptedSecrets) *OAuthRegistry {
	return &OAuthRegistry{
		providers: map[string]OAuthProvider{},
		state:     newOAuthStateStore(10 * time.Minute),
		secrets:   secrets,
		BaseURL:   strings.TrimRight(baseURL, "/"),
	}
}

// Register adds a provider to the registry. Calling Register twice
// with the same name overwrites — the last-write-wins lets a
// deployment override built-in scope defaults by re-registering, and
// also lets the admin OAuth setup endpoint swap credentials in at
// runtime without a daemon restart.
func (r *OAuthRegistry) Register(p OAuthProvider) {
	r.providers[p.Name] = p
}

// Unregister removes a provider from the registry. Used by the admin
// "clear credentials" path so a deployment can take a provider out of
// service without restarting. Idempotent — unregistering a missing
// provider is a no-op.
func (r *OAuthRegistry) Unregister(name string) {
	delete(r.providers, name)
}

// Provider returns the OAuthProvider registered under name, plus a
// presence flag. Lets the admin endpoint introspect what's currently
// configured without exposing the whole map.
func (r *OAuthRegistry) Provider(name string) (OAuthProvider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Providers returns the registered provider names, sorted, for the
// UI's "connect a service" picker.
func (r *OAuthRegistry) Providers() []string {
	out := make([]string, 0, len(r.providers))
	for k := range r.providers {
		out = append(out, k)
	}
	// Stable order matters for the UI; sort.Strings is overkill for
	// the typical 5-10 entries, just do it inline.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (r *OAuthRegistry) httpClient() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return http.DefaultClient
}

// redirectURI builds the URL the provider should send the user
// back to. The provider's OAuth app config must list this exact
// URL or the callback fails with a generic provider error.
func (r *OAuthRegistry) redirectURI(provider string) string {
	return r.BaseURL + "/api/v1/oauth/" + url.PathEscape(provider) + "/callback"
}

// secretNameFor builds the encrypted-secrets key under which a
// (provider, account) pair's tokens live. Format intentionally
// matches the validator's allowed character set ([A-Za-z0-9_.-]).
func secretNameFor(provider, account string) string {
	if account == "" {
		account = "default"
	}
	return "oauth." + provider + "." + account
}

// ----- token storage shape -------------------------------------------

// StoredOAuthToken is the JSON shape we marshal into the encrypted
// secret store. Kept narrow on purpose: just the fields a downstream
// drop needs to make API calls. Provider-specific extras (Slack's
// `team_id`, GitHub's `scope` list, etc.) are stored as-is in
// `extras` so a custom drop can read them without us having to
// extend the struct per provider.
type StoredOAuthToken struct {
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token,omitempty"`
	TokenType    string         `json:"token_type,omitempty"`
	ExpiresAt    *time.Time     `json:"expires_at,omitempty"`
	Scope        string         `json:"scope,omitempty"`
	ObtainedAt   time.Time      `json:"obtained_at"`
	Extras       map[string]any `json:"extras,omitempty"`
}

// providerTokenResponse mirrors the standard OAuth 2.0 token
// response. Most providers conform; outliers go through Extras.
type providerTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// ----- state store ---------------------------------------------------

// pendingOAuth holds one in-flight authorization. Tied to a state
// token; deleted on callback or expiry.
type pendingOAuth struct {
	tenant   string
	provider string
	account  string
	returnTo string
	created  time.Time
}

// oauthStateStore is a TTL-bounded in-memory map keyed by state
// tokens. Process-local on purpose — pending OAuth flows are short-
// lived (the user is mid-redirect) and survival across daemon
// restarts isn't required. A multi-replica deployment with a
// load-balancer that doesn't sticky-route OAuth callbacks would
// need a shared store; that's T3 work.
type oauthStateStore struct {
	mu      sync.Mutex
	pending map[string]pendingOAuth
	ttl     time.Duration
}

func newOAuthStateStore(ttl time.Duration) *oauthStateStore {
	return &oauthStateStore{
		pending: map[string]pendingOAuth{},
		ttl:     ttl,
	}
}

// mint adds an entry and returns a fresh state token (32 random
// bytes hex-encoded — plenty of entropy, fits in a URL).
func (s *oauthStateStore) mint(p pendingOAuth) (string, error) {
	tok := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, tok); err != nil {
		return "", err
	}
	state := hex.EncodeToString(tok)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now())
	p.created = time.Now()
	s.pending[state] = p
	return state, nil
}

// consume returns and DELETES the pending entry for state, or
// (zero, false) if missing or expired. Single-use by design — a
// stolen state token can't be replayed.
func (s *oauthStateStore) consume(state string) (pendingOAuth, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now())
	p, ok := s.pending[state]
	if !ok {
		return pendingOAuth{}, false
	}
	delete(s.pending, state)
	return p, true
}

// sweepLocked drops expired entries. Called inline from mint/consume
// so we never accumulate stale state.
func (s *oauthStateStore) sweepLocked(now time.Time) {
	for k, v := range s.pending {
		if now.Sub(v.created) > s.ttl {
			delete(s.pending, k)
		}
	}
}

// ----- token exchange ------------------------------------------------

// exchangeCode is the POST to the provider's token endpoint that
// turns an authorization code into access/refresh tokens. Standard
// OAuth 2.0 — sends client_id + client_secret + code +
// redirect_uri + grant_type as form-urlencoded; most providers
// return JSON.
func (r *OAuthRegistry) exchangeCode(ctx context.Context, p OAuthProvider, code string) (*StoredOAuthToken, error) {
	form := url.Values{}
	form.Set("client_id", p.ClientID)
	form.Set("client_secret", p.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", r.redirectURI(p.Name))
	form.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Truncate the body in the error so we don't dump huge HTML
		// error pages into logs/UI alerts.
		excerpt := string(body)
		if len(excerpt) > 512 {
			excerpt = excerpt[:512] + "…"
		}
		return nil, fmt.Errorf("token exchange returned %d: %s", resp.StatusCode, excerpt)
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	var typed providerTokenResponse
	_ = json.Unmarshal(body, &typed) // best-effort; raw is the source of truth

	if typed.AccessToken == "" {
		// Some providers (e.g. Slack) put a per-call error in the JSON
		// body even at HTTP 200. Surface it explicitly.
		if msg, ok := raw["error"].(string); ok && msg != "" {
			return nil, fmt.Errorf("provider returned error: %s", msg)
		}
		return nil, fmt.Errorf("no access_token in response: %s", string(body))
	}

	tok := &StoredOAuthToken{
		AccessToken:  typed.AccessToken,
		RefreshToken: typed.RefreshToken,
		TokenType:    typed.TokenType,
		Scope:        typed.Scope,
		ObtainedAt:   time.Now().UTC(),
	}
	if typed.ExpiresIn > 0 {
		t := tok.ObtainedAt.Add(time.Duration(typed.ExpiresIn) * time.Second)
		tok.ExpiresAt = &t
	}
	// Stash anything the standard struct didn't capture (Slack's
	// team_id, bot_user_id, etc.) under Extras so provider-specific
	// drops can read it without us extending StoredOAuthToken per
	// service.
	tok.Extras = make(map[string]any, len(raw))
	for k, v := range raw {
		switch k {
		case "access_token", "refresh_token", "token_type", "expires_in", "scope":
			// already typed
		default:
			tok.Extras[k] = v
		}
	}
	return tok, nil
}

// store writes the token under the (tenant, provider, account)
// convention. Returns the secret name so callers can echo it back
// to the user ("connected as oauth.slack.main").
func (r *OAuthRegistry) store(ctx context.Context, tenant, provider, account string, tok *StoredOAuthToken) (string, error) {
	if r.secrets == nil {
		return "", errors.New("encrypted secret store not configured")
	}
	name := secretNameFor(provider, account)
	payload, err := json.Marshal(tok)
	if err != nil {
		return "", fmt.Errorf("marshal token: %w", err)
	}
	if err := r.secrets.Put(ctx, tenant, name, string(payload)); err != nil {
		return "", err
	}
	return name, nil
}

// GetOAuthToken fetches a previously-stored token by (provider,
// account). Tenant comes from the context (set by Engine.RunNode
// via core.WithTenant). The returned struct is the StoredOAuthToken
// shape — caller pulls .AccessToken to make API calls.
//
// Refresh-on-expiry: NOT implemented in v1. The token is whatever
// was last stored. If it's expired the API call fails and the user
// re-connects. v2 will check ExpiresAt and refresh transparently.
func (r *OAuthRegistry) GetOAuthToken(ctx context.Context, provider, account string) (*StoredOAuthToken, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, errors.New("get oauth token: no tenant in context")
	}
	name := secretNameFor(provider, account)
	raw, err := r.secrets.Get(core.WithTenant(ctx, tenant), name)
	if err != nil {
		return nil, fmt.Errorf("oauth.%s.%s: %w", provider, account, err)
	}
	var tok StoredOAuthToken
	if err := json.Unmarshal([]byte(raw), &tok); err != nil {
		return nil, fmt.Errorf("unmarshal stored token: %w", err)
	}
	return &tok, nil
}

