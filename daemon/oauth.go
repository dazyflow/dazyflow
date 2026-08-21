// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
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
//
// What it DOES handle:
//
//   - Refresh-on-expiry: GetOAuthToken transparently exchanges a stored
//     refresh_token for a fresh access token when the current one is at
//     or past expiry, so long-running scheduled flows keep working
//     without the user reconnecting. See GetOAuthToken / refreshAccessToken.

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

	// TokenAuthStyle selects how client credentials are presented to the
	// token endpoint. Empty (the default for every existing provider) means
	// client_secret_post — client_id + client_secret in the form body.
	// "basic" means client_secret_basic — an HTTP Basic Authorization header
	// carrying client_id:client_secret, with neither in the body. Fortnox
	// requires "basic"; most other providers accept post.
	TokenAuthStyle string
}

// OAuthRegistry holds the set of providers the daemon can drive,
// plus the in-process state store and the encrypted secrets backend
// it writes tokens into.
type OAuthRegistry struct {
	// mu guards the providers map: Register/Unregister mutate it at runtime
	// (admin OAuth setup + marketplace install/uninstall) while the OAuth
	// authorize/callback handlers read it concurrently — an unguarded map is a
	// concurrent read/write crash.
	mu        sync.RWMutex
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

	// refreshMu guards refreshLocks; each per-account lock serializes
	// concurrent refreshes of the same token so a fleet of scheduled
	// flows firing at once makes one refresh call, not N.
	refreshMu    sync.Mutex
	refreshLocks map[string]*sync.Mutex
}

// NewOAuthRegistry constructs a registry. baseURL is the
// externally-reachable origin (no trailing slash). secrets must be
// non-nil — that's where exchanged tokens land.
func NewOAuthRegistry(baseURL string, secrets *EncryptedSecrets) *OAuthRegistry {
	return &OAuthRegistry{
		providers:    map[string]OAuthProvider{},
		state:        newOAuthStateStore(10 * time.Minute),
		secrets:      secrets,
		BaseURL:      strings.TrimRight(baseURL, "/"),
		refreshLocks: map[string]*sync.Mutex{},
	}
}

// Register adds a provider to the registry. Calling Register twice
// with the same name overwrites — the last-write-wins lets a
// deployment override built-in scope defaults by re-registering, and
// also lets the admin OAuth setup endpoint swap credentials in at
// runtime without a daemon restart.
func (r *OAuthRegistry) Register(p OAuthProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name] = p
}

// Unregister removes a provider from the registry. Used by the admin
// "clear credentials" path so a deployment can take a provider out of
// service without restarting. Idempotent — unregistering a missing
// provider is a no-op.
func (r *OAuthRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.providers, name)
}

// Provider returns the OAuthProvider registered under name, plus a
// presence flag. Lets the admin endpoint introspect what's currently
// configured without exposing the whole map.
func (r *OAuthRegistry) Provider(name string) (OAuthProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// Providers returns the registered provider names, sorted, for the
// UI's "connect a service" picker.
func (r *OAuthRegistry) Providers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.providers))
	for k := range r.providers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (r *OAuthRegistry) httpClient() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return defaultOAuthClient
}

// defaultOAuthClient bounds provider token-endpoint calls. http.DefaultClient
// has no timeout, so a slow or hostile token endpoint would hang the calling
// goroutine for as long as it stalls — and token *refresh* runs on the job
// execution path, sometimes under a context with no deadline of its own. A
// fixed ceiling guarantees the call returns.
var defaultOAuthClient = &http.Client{Timeout: 30 * time.Second}

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
	// binding ties the flow to the browser that started it (RFC 6749
	// §10.12). When non-empty, the callback requires a matching value in the
	// dz_oauth_state cookie — without this, an attacker can complete a flow
	// they started and inject their own provider account into a victim's org.
	// Empty for the JSON/manual authorize path (the link is handed to a
	// different agent), which relies on the unguessable single-use state.
	binding string
	created time.Time
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
	return r.postTokenForm(ctx, p, form)
}

// postTokenForm POSTs a form-urlencoded request to the provider's token
// endpoint and parses the standard OAuth 2.0 response into a stored
// token. Shared by the authorization_code exchange and the
// refresh_token grant — they differ only in the form they send.
func (r *OAuthRegistry) postTokenForm(ctx context.Context, p OAuthProvider, form url.Values) (*StoredOAuthToken, error) {
	// client_secret_basic: the credentials move from the body into an HTTP
	// Basic header. Strip them from the form first — sending them both ways
	// makes strict servers (Fortnox) reject the request as ambiguous. The
	// callers (exchangeCode / refreshAccessToken) always set them in the
	// form, so this branch is the single place that undoes that for basic.
	basicAuth := p.TokenAuthStyle == "basic"
	if basicAuth {
		form.Del("client_id")
		form.Del("client_secret")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basicAuth {
		req.SetBasicAuth(p.ClientID, p.ClientSecret)
	}

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
	// A freshly stored token is the fix for a dead grant — forget the marker
	// so the account stops asking to be reconnected.
	r.clearReconnectNeeded(ctx, tenant, provider, account)
	return name, nil
}

// reconnectNeededPrefix marks an account whose stored grant no longer works:
// the refresh exchange came back with a definitive rejection (the user revoked
// access, changed their password, or the grant simply expired). Written the
// moment we learn it, cleared as soon as a token is stored or refreshed again.
//
// Without it the Apps page can only report whether a token EXISTS, so a dead
// Google grant reads as "connected" and the one thing the user needs to do —
// reconnect that account — is the one thing the page doesn't offer.
const reconnectNeededPrefix = "oauthfail."

func reconnectNeededName(provider, account string) string {
	if account == "" {
		account = "default"
	}
	return reconnectNeededPrefix + provider + "." + account
}

// noteReconnectNeeded records that this account's grant is dead. Best effort:
// failing to write the marker must never turn a degraded flow into a broken
// one, so errors are logged and swallowed.
func (r *OAuthRegistry) noteReconnectNeeded(ctx context.Context, tenant, provider, account string, cause error) {
	if r.secrets == nil {
		return
	}
	payload, merr := json.Marshal(map[string]any{
		"at":    time.Now().UTC().Format(time.RFC3339),
		"error": cause.Error(),
	})
	if merr != nil {
		return
	}
	if err := r.secrets.Put(ctx, tenant, reconnectNeededName(provider, account), string(payload)); err != nil {
		log.Printf("oauth: could not record that %s/%s needs reconnecting (tenant %s): %v", provider, account, tenant, err)
	}
}

// clearReconnectNeeded forgets the marker once the account works again.
func (r *OAuthRegistry) clearReconnectNeeded(ctx context.Context, tenant, provider, account string) {
	if r.secrets == nil {
		return
	}
	_ = r.secrets.Delete(ctx, tenant, reconnectNeededName(provider, account))
}

// RefreshStaleAccounts gives every account whose access token has already
// expired the refresh the next run would do anyway — just sooner. A rejected
// refresh records the dead grant, a successful one clears it and stores the
// fresh token, so the caller can then report the truth.
//
// It exists because the marker alone is only written when something USES the
// token. That left the Apps page saying "connected" until the next scheduled
// run happened to fail, which is precisely the moment the user is on the page
// asking why their flow is broken. An account whose token is still valid
// costs nothing here: no network call, nothing to say.
func (r *OAuthRegistry) RefreshStaleAccounts(ctx context.Context, tenant, provider string, accounts []string) {
	for _, a := range accounts {
		tok, err := r.loadToken(ctx, tenant, provider, a)
		if err != nil || !tokenNeedsRefresh(tok) {
			// No token, or one that is still good — the next use would not
			// refresh either, so there is nothing to learn.
			continue
		}
		if _, rerr := r.refreshAccessToken(ctx, tenant, provider, a, tok); rerr != nil {
			r.noteReconnectNeeded(ctx, tenant, provider, a, rerr)
			continue
		}
		r.clearReconnectNeeded(ctx, tenant, provider, a)
	}
}

// ReconnectNeeded reports which of the given accounts have a dead grant.
func (r *OAuthRegistry) ReconnectNeeded(ctx context.Context, tenant, provider string, accounts []string) []string {
	if r.secrets == nil || len(accounts) == 0 {
		return nil
	}
	var out []string
	for _, a := range accounts {
		if v, err := r.secrets.GetExact(ctx, tenant, reconnectNeededName(provider, a)); err == nil && v != "" {
			out = append(out, a)
		}
	}
	return out
}

// refreshSkew is how far before the stored expiry we proactively
// refresh. Provider clocks and network latency mean a token "valid for
// another 10 seconds" is a coin-flip by the time the API call lands, so
// we treat anything inside this window as already expired.
const refreshSkew = 60 * time.Second

// GetOAuthToken fetches a previously-stored token by (provider,
// account). Tenant comes from the context (set by Engine.RunNode
// via core.WithTenant). The returned struct is the StoredOAuthToken
// shape — caller pulls .AccessToken to make API calls.
//
// Refresh-on-expiry: when the stored access token is at or past its
// expiry (within refreshSkew) and we hold a refresh_token plus a
// configured provider, we exchange the refresh_token for a fresh access
// token, persist it, and return the new one — so long-running scheduled
// flows keep working without the user reconnecting every hour. If the
// refresh fails we fall back to the stored token (the API call will
// surface the real error) rather than hard-failing the lookup.
func (r *OAuthRegistry) GetOAuthToken(ctx context.Context, provider, account string) (*StoredOAuthToken, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, errors.New("get oauth token: no tenant in context")
	}
	tok, err := r.loadToken(ctx, tenant, provider, account)
	if err != nil {
		return nil, err
	}
	if !tokenNeedsRefresh(tok) {
		return tok, nil
	}
	refreshed, err := r.refreshAccessToken(ctx, tenant, provider, account, tok)
	if err != nil {
		// Best-effort: hand back what we have. The downstream API call
		// returns the authoritative 401, and the user can reconnect.
		log.Printf("oauth refresh failed for %s/%s (tenant %s): %v; using stored token", provider, account, tenant, err)
		// Remember it, though. This is the moment — and the only moment —
		// where we know the grant itself is dead rather than the call being
		// unlucky, so it is what lets the Apps page say "reconnect this
		// account" instead of showing it as healthy.
		r.noteReconnectNeeded(ctx, tenant, provider, account, err)
		return tok, nil
	}
	r.clearReconnectNeeded(ctx, tenant, provider, account)
	return refreshed, nil
}

// loadToken reads and decodes the stored token for (tenant, provider,
// account) without any refresh.
func (r *OAuthRegistry) loadToken(ctx context.Context, tenant, provider, account string) (*StoredOAuthToken, error) {
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

// tokenNeedsRefresh reports whether a stored token is expired (within
// the skew window) AND refreshable. A token with no expiry recorded is
// treated as non-expiring (some providers issue long-lived tokens and
// omit expires_in); without a refresh_token there's nothing to do.
func tokenNeedsRefresh(tok *StoredOAuthToken) bool {
	if tok.RefreshToken == "" || tok.ExpiresAt == nil {
		return false
	}
	return time.Now().UTC().Add(refreshSkew).After(*tok.ExpiresAt)
}

// refreshAccessToken exchanges the stored refresh_token for a fresh
// access token, persists it, and returns it. A per-account lock plus a
// re-check after acquiring it means a burst of concurrent flows for the
// same account makes a single refresh call — the rest see the freshly
// stored token and skip the network round-trip.
func (r *OAuthRegistry) refreshAccessToken(ctx context.Context, tenant, provider, account string, current *StoredOAuthToken) (*StoredOAuthToken, error) {
	p, ok := r.Provider(provider)
	if !ok || p.ClientID == "" || p.TokenURL == "" {
		return nil, fmt.Errorf("provider %q not configured for refresh", provider)
	}

	lock := r.refreshLock(secretNameFor(provider, account))
	lock.Lock()
	defer lock.Unlock()

	// Re-check under the lock: another goroutine may have refreshed
	// while we waited.
	if latest, err := r.loadToken(ctx, tenant, provider, account); err == nil && !tokenNeedsRefresh(latest) {
		return latest, nil
	}

	form := url.Values{}
	form.Set("client_id", p.ClientID)
	form.Set("client_secret", p.ClientSecret)
	form.Set("refresh_token", current.RefreshToken)
	form.Set("grant_type", "refresh_token")

	fresh, err := r.postTokenForm(ctx, p, form)
	if err != nil {
		return nil, err
	}
	// Providers (Google among them) usually omit the refresh_token on a
	// refresh response — the original one stays valid, so carry it over.
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = current.RefreshToken
	}
	// Likewise preserve provider-specific Extras (team_id, etc.) the
	// refresh response doesn't echo back.
	if len(fresh.Extras) == 0 && len(current.Extras) > 0 {
		fresh.Extras = current.Extras
	}
	if _, err := r.store(ctx, tenant, provider, account, fresh); err != nil {
		return nil, fmt.Errorf("persist refreshed token: %w", err)
	}
	return fresh, nil
}

// refreshLock returns the per-account mutex, creating it on first use.
func (r *OAuthRegistry) refreshLock(name string) *sync.Mutex {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	m, ok := r.refreshLocks[name]
	if !ok {
		m = &sync.Mutex{}
		r.refreshLocks[name] = m
	}
	return m
}
