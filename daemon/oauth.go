// SPDX-FileCopyrightText: 2026 Angels' Ware
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

	"github.com/dazyflow/dazyflow/core"

	"github.com/dazyflow/dazyflow/auth"
)

// The daemon holds the client secret and exchanges the code, so the browser never
// sees either; the resulting token is sealed into the tenant's secret store.

type OAuthProvider struct {
	Name         string   // e.g. "slack", "github" — matches the URL slug
	AuthorizeURL string   // provider's authorize endpoint
	TokenURL     string   // provider's code-exchange endpoint
	Scopes       []string // OAuth scopes to request; provider-specific shape
	ClientID     string   // OAuth client_id (provider-issued)
	ClientSecret string   // OAuth client_secret (keep out of logs)

	AuthorizeExtras map[string]string

	// Providers disagree on where the client credentials go.
	TokenAuthStyle string
}

type OAuthRegistry struct {
	// Register and Unregister mutate it at runtime, so reads must hold it.
	mu        sync.RWMutex
	providers map[string]OAuthProvider
	state     *oauthStateStore
	secrets   *EncryptedSecrets

	// Must be the externally-reachable origin: it is what the provider redirects to.
	BaseURL string

	HTTPClient *http.Client

	// Per-account lock, so concurrent runs do not each refresh the same token.
	refreshMu    sync.Mutex
	refreshLocks map[string]*sync.Mutex
}

func NewOAuthRegistry(baseURL string, secrets *EncryptedSecrets) *OAuthRegistry {
	return &OAuthRegistry{
		providers:    map[string]OAuthProvider{},
		state:        newOAuthStateStore(10 * time.Minute),
		secrets:      secrets,
		BaseURL:      strings.TrimRight(baseURL, "/"),
		refreshLocks: map[string]*sync.Mutex{},
	}
}

// Registering twice replaces the earlier provider.
func (r *OAuthRegistry) Register(p OAuthProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name] = p
}

func (r *OAuthRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.providers, name)
}

func (r *OAuthRegistry) Provider(name string) (OAuthProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

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

// http.DefaultClient has no timeout, so a hung provider would pin the caller.
var defaultOAuthClient = &http.Client{Timeout: 30 * time.Second}

func (r *OAuthRegistry) redirectURI(provider string) string {
	return r.BaseURL + "/api/v1/oauth/" + url.PathEscape(provider) + "/callback"
}

func secretNameFor(provider, account string) string {
	if account == "" {
		account = "default"
	}
	return "oauth." + provider + "." + account
}

type StoredOAuthToken struct {
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token,omitempty"`
	TokenType    string         `json:"token_type,omitempty"`
	ExpiresAt    *time.Time     `json:"expires_at,omitempty"`
	Scope        string         `json:"scope,omitempty"`
	ObtainedAt   time.Time      `json:"obtained_at"`
	Extras       map[string]any `json:"extras,omitempty"`
}

type providerTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

type pendingOAuth struct {
	tenant   string
	provider string
	account  string
	returnTo string
	binding  string
	host     string
	created  time.Time
}

type pendingOAuthWire struct {
	Tenant   string    `json:"tenant"`
	Provider string    `json:"provider"`
	Account  string    `json:"account"`
	ReturnTo string    `json:"return_to"`
	Binding  string    `json:"binding"`
	Host     string    `json:"host,omitempty"`
	Created  time.Time `json:"created"`
}

func (p pendingOAuth) wire() pendingOAuthWire {
	return pendingOAuthWire{
		Tenant: p.tenant, Provider: p.provider, Account: p.account,
		ReturnTo: p.returnTo, Binding: p.binding, Host: p.host, Created: p.created,
	}
}

func (w pendingOAuthWire) pending() pendingOAuth {
	return pendingOAuth{
		tenant: w.Tenant, provider: w.Provider, account: w.Account,
		returnTo: w.ReturnTo, binding: w.Binding, host: w.Host, created: w.Created,
	}
}

type oauthStateStore struct {
	store auth.EphemeralStore
	ttl   time.Duration
}

func newOAuthStateStore(ttl time.Duration) *oauthStateStore {
	return &oauthStateStore{store: auth.NewMemEphemeralStore(), ttl: ttl}
}

func (s *oauthStateStore) setStore(store auth.EphemeralStore) {
	if store != nil {
		s.store = store
	}
}

func (s *oauthStateStore) mint(p pendingOAuth) (string, error) {
	tok := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, tok); err != nil {
		return "", err
	}
	state := hex.EncodeToString(tok)
	p.created = time.Now()
	if err := putEphemeral(context.Background(), s.store,
		auth.EphemeralOAuthPending, state, p.wire(), p.created.Add(s.ttl)); err != nil {
		return "", err
	}
	return state, nil
}

func (s *oauthStateStore) consume(state string) (pendingOAuth, bool) {
	w, ok := consumeEphemeral[pendingOAuthWire](context.Background(), s.store, auth.EphemeralOAuthPending, state)
	if !ok {
		return pendingOAuth{}, false
	}
	return w.pending(), true
}

func (r *OAuthRegistry) exchangeCode(ctx context.Context, p OAuthProvider, code string) (*StoredOAuthToken, error) {
	form := url.Values{}
	form.Set("client_id", p.ClientID)
	form.Set("client_secret", p.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", r.redirectURI(p.Name))
	form.Set("grant_type", "authorization_code")
	return r.postTokenForm(ctx, p, form)
}

func (r *OAuthRegistry) postTokenForm(ctx context.Context, p OAuthProvider, form url.Values) (*StoredOAuthToken, error) {
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
	tok.Extras = make(map[string]any, len(raw))
	for k, v := range raw {
		switch k {
		case "access_token", "refresh_token", "token_type", "expires_in", "scope":
		default:
			tok.Extras[k] = v
		}
	}
	return tok, nil
}

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
	r.clearReconnectNeeded(ctx, tenant, provider, account)
	return name, nil
}

const reconnectNeededPrefix = "oauthfail."

func reconnectNeededName(provider, account string) string {
	if account == "" {
		account = "default"
	}
	return reconnectNeededPrefix + provider + "." + account
}

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

func (r *OAuthRegistry) clearReconnectNeeded(ctx context.Context, tenant, provider, account string) {
	if r.secrets == nil {
		return
	}
	_ = r.secrets.Delete(ctx, tenant, reconnectNeededName(provider, account))
}

func (r *OAuthRegistry) RefreshStaleAccounts(ctx context.Context, tenant, provider string, accounts []string) {
	for _, a := range accounts {
		tok, err := r.loadToken(ctx, tenant, provider, a)
		if err != nil || !tokenNeedsRefresh(tok) {
			continue
		}
		if _, rerr := r.refreshAccessToken(ctx, tenant, provider, a, tok); rerr != nil {
			r.noteReconnectNeeded(ctx, tenant, provider, a, rerr)
			continue
		}
		r.clearReconnectNeeded(ctx, tenant, provider, a)
	}
}

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

const refreshSkew = 60 * time.Second

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
		log.Printf("oauth refresh failed for %s/%s (tenant %s): %v; using stored token", provider, account, tenant, err)
		r.noteReconnectNeeded(ctx, tenant, provider, account, err)
		return tok, nil
	}
	r.clearReconnectNeeded(ctx, tenant, provider, account)
	return refreshed, nil
}

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

func tokenNeedsRefresh(tok *StoredOAuthToken) bool {
	if tok.RefreshToken == "" || tok.ExpiresAt == nil {
		return false
	}
	return time.Now().UTC().Add(refreshSkew).After(*tok.ExpiresAt)
}

func (r *OAuthRegistry) refreshAccessToken(ctx context.Context, tenant, provider, account string, current *StoredOAuthToken) (*StoredOAuthToken, error) {
	p, ok := r.Provider(provider)
	if !ok || p.ClientID == "" || p.TokenURL == "" {
		return nil, fmt.Errorf("provider %q not configured for refresh", provider)
	}

	lock := r.refreshLock(secretNameFor(provider, account))
	lock.Lock()
	defer lock.Unlock()

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
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = current.RefreshToken
	}
	if len(fresh.Extras) == 0 && len(current.Extras) > 0 {
		fresh.Extras = current.Extras
	}
	if _, err := r.store(ctx, tenant, provider, account, fresh); err != nil {
		return nil, fmt.Errorf("persist refreshed token: %w", err)
	}
	return fresh, nil
}

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

func (r *OAuthRegistry) SetEphemeralStore(s auth.EphemeralStore) {
	r.state.setStore(s)
}
