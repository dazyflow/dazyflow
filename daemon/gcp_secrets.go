// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// GcpSecretsProvider is the Google Cloud flavour of the BYO secret manager
// (see VaultProvider for the model): it resolves ${gcp.NAME} /
// ${gcp.NAME#field} against each TENANT's own GCP Secret Manager. NAME is
// the secret id within the configured project; "versions/latest" is always
// accessed. The optional #field plucks one key out of a JSON payload.
//
// Auth is a service-account key: the daemon signs an RS256 JWT and exchanges
// it for an OAuth access token at the key's token_uri (cached until near
// expiry). Hand-rolled over Google's REST API rather than pulling in
// cloud.google.com/go + google.golang.org/api — the provider needs one GET
// and one token exchange; same dependency trade as the AWS provider.
type GcpSecretsProvider struct {
	client *gcpAPIClient
	// loadConfig returns the calling tenant's connection config. ok=false
	// means the tenant hasn't configured GCP.
	loadConfig func(ctx context.Context, tenant string) (cfg GcpSecretsConfig, ok bool, err error)
	cache      *tenantSecretCache
}

func NewGcpSecretsProvider(client *gcpAPIClient, loadConfig func(context.Context, string) (GcpSecretsConfig, bool, error), ttl time.Duration) *GcpSecretsProvider {
	return &GcpSecretsProvider{client: client, loadConfig: loadConfig, cache: newTenantSecretCache(ttl)}
}

func NewGcpSecretsProviderForStore(es *EncryptedSecrets, httpTimeout time.Duration) *GcpSecretsProvider {
	return NewGcpSecretsProvider(
		newGcpAPIClient(httpTimeout),
		func(ctx context.Context, tenant string) (GcpSecretsConfig, bool, error) {
			return loadProviderConfig[GcpSecretsConfig](ctx, es, tenant, gcpConfigSecretName)
		},
		0,
	)
}

// VerifyGcpConfig validates a config and checks the service-account key
// actually mints a token and the project answers. Used by the save endpoint.
func VerifyGcpConfig(ctx context.Context, cfg GcpSecretsConfig, timeout time.Duration) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	return newGcpAPIClient(timeout).verify(ctx, cfg)
}

func (p *GcpSecretsProvider) Scheme() string { return "gcp" }

// Get resolves "NAME" or "NAME#field" against the tenant's configured
// project. The tenant comes from context — a BYO secret is always
// tenant-scoped, never global.
func (p *GcpSecretsProvider) Get(ctx context.Context, ref string) (string, error) {
	return getCloudSecret(ctx, "gcp", ref, p.cache, p.loadConfig, p.client.accessSecret)
}

// GcpSecretsConfig is one tenant's connection to GCP Secret Manager: the
// project plus a pasted service-account key file (JSON). The key carries
// the private key, so the config is itself a secret — stored encrypted.
type GcpSecretsConfig struct {
	ProjectID string `json:"project_id"`
	// ServiceAccountKey is the full JSON key file from the GCP console
	// (IAM → service accounts → keys). client_email, private_key, and
	// token_uri are read from it.
	ServiceAccountKey string `json:"service_account_key"`
	// Endpoint overrides the API host — tests. Empty uses
	// https://secretmanager.googleapis.com.
	Endpoint string `json:"endpoint,omitempty"`
}

// gcpServiceAccountKey is the slice of the key file the auth flow needs.
type gcpServiceAccountKey struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

func (c GcpSecretsConfig) validate() error {
	if c.ProjectID == "" {
		return fmt.Errorf("project_id is required")
	}
	if c.Endpoint != "" && !strings.HasPrefix(c.Endpoint, "https://") && !strings.HasPrefix(c.Endpoint, "http://") {
		return fmt.Errorf("endpoint must be an http(s) URL")
	}
	_, err := c.key()
	return err
}

func (c GcpSecretsConfig) key() (gcpServiceAccountKey, error) {
	var k gcpServiceAccountKey
	if err := json.Unmarshal([]byte(c.ServiceAccountKey), &k); err != nil {
		return k, fmt.Errorf("service_account_key is not a valid JSON key file")
	}
	if k.ClientEmail == "" || k.PrivateKey == "" {
		return k, fmt.Errorf("service_account_key must contain client_email and private_key")
	}
	if k.TokenURI == "" {
		k.TokenURI = "https://oauth2.googleapis.com/token"
	}
	return k, nil
}

func (c GcpSecretsConfig) endpointURL() string {
	if c.Endpoint != "" {
		return strings.TrimRight(c.Endpoint, "/")
	}
	return "https://secretmanager.googleapis.com"
}

// gcpConfigSecretName is the reserved encrypted-store key for a tenant's GCP
// connection (the "cfg:" prefix hides it from user-facing listings).
const gcpConfigSecretName = "cfg:secret-manager-gcp"

// gcpAPIClient speaks Secret Manager's REST API with service-account JWT →
// OAuth token auth. Tokens are cached per (token_uri, client_email) until
// shortly before expiry — same pattern as the Vault client's AppRole cache.
// Both the API endpoint and token_uri are tenant-supplied, so the client routes
// through the shared SSRF guard (post-DNS, rebinding-resistant; a no-op when the
// operator opted into private egress) — defense in depth so the verify/fetch
// call can't be turned into an internal-network or metadata probe.
type gcpAPIClient struct {
	httpc *http.Client

	mu     sync.Mutex
	tokens map[string]appRoleToken // reuse the {token, exp} shape
}

func newGcpAPIClient(timeout time.Duration) *gcpAPIClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &gcpAPIClient{httpc: hfnet.SafeHTTPClient(timeout, hfnet.PrivateEgressAllowed()), tokens: map[string]appRoleToken{}}
}

func (c *gcpAPIClient) accessSecret(ctx context.Context, cfg GcpSecretsConfig, name string) (string, error) {
	tok, err := c.token(ctx, cfg)
	if err != nil {
		return "", err
	}
	apiURL := fmt.Sprintf("%s/v1/projects/%s/secrets/%s/versions/latest:access",
		cfg.endpointURL(), url.PathEscape(cfg.ProjectID), url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("gcp returned %d: %s", resp.StatusCode, extractGcpError(raw))
	}
	var out struct {
		Payload struct {
			Data string `json:"data"` // base64 (std)
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(out.Payload.Data)
	if err != nil {
		return "", fmt.Errorf("decode payload: %w", err)
	}
	return string(data), nil
}

// verify mints a token (proves the key parses, signs, and the token endpoint
// accepts it), then accesses a probe secret: 404/403 prove the project
// answered an authenticated request; 401 means the token was rejected.
func (c *gcpAPIClient) verify(ctx context.Context, cfg GcpSecretsConfig) error {
	if _, err := c.token(ctx, cfg); err != nil {
		return err
	}
	_, err := c.accessSecret(ctx, cfg, "dazyflow-connection-test")
	if err == nil {
		return nil // probe actually exists — fine
	}
	msg := err.Error()
	if strings.Contains(msg, "gcp returned 404") || strings.Contains(msg, "gcp returned 403") {
		return nil
	}
	return err
}

// token returns a cached or freshly-exchanged OAuth access token for cfg's
// service account.
func (c *gcpAPIClient) token(ctx context.Context, cfg GcpSecretsConfig) (string, error) {
	key, err := cfg.key()
	if err != nil {
		return "", err
	}
	cacheKey := key.TokenURI + "|" + key.ClientEmail
	c.mu.Lock()
	if t, ok := c.tokens[cacheKey]; ok && t.exp.After(time.Now()) {
		c.mu.Unlock()
		return t.token, nil
	}
	c.mu.Unlock()

	assertion, err := signGcpJWT(key, nowFunc().UTC())
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, key.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("token exchange returned %d: %s", resp.StatusCode, extractGcpError(raw))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("token exchange returned no access_token")
	}
	// Renew a little before the token actually expires.
	lifetime := time.Duration(out.ExpiresIn)*time.Second - 30*time.Second
	if lifetime < 0 {
		lifetime = 0
	}
	c.mu.Lock()
	c.tokens[cacheKey] = appRoleToken{token: out.AccessToken, exp: time.Now().Add(lifetime)}
	c.mu.Unlock()
	return out.AccessToken, nil
}

// signGcpJWT builds the RS256 service-account assertion for the OAuth
// jwt-bearer grant (https://developers.google.com/identity/protocols/oauth2/service-account).
func signGcpJWT(key gcpServiceAccountKey, now time.Time) (string, error) {
	priv, err := parseGcpPrivateKey(key.PrivateKey)
	if err != nil {
		return "", err
	}
	b64 := func(v any) (string, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(b), nil
	}
	header, err := b64(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := b64(map[string]any{
		"iss":   key.ClientEmail,
		"scope": "https://www.googleapis.com/auth/cloud-platform",
		"aud":   key.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + claims
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign assertion: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parseGcpPrivateKey decodes the key file's PEM private key (PKCS#8 in
// every key Google issues today; PKCS#1 accepted for completeness).
func parseGcpPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("private_key is not valid PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
		return nil, fmt.Errorf("private_key is not an RSA key")
	}
	if rk, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return rk, nil
	}
	return nil, fmt.Errorf("private_key did not parse as PKCS#8 or PKCS#1")
}

// extractGcpError pulls error.message out of a Google API error body.
func extractGcpError(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	// OAuth endpoint errors use {error, error_description}.
	var oe struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &oe); err == nil && oe.Error != "" {
		if oe.Description != "" {
			return oe.Error + ": " + oe.Description
		}
		return oe.Error
	}
	return truncateForError(body)
}

var _ core.SecretProvider = (*GcpSecretsProvider)(nil)
