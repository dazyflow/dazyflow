// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package roaring hosts the native Roaring connector — Roaring.io is the Nordic
// company- and person-data enrichment service (org-number → company overview,
// name → candidate companies, credit/risk data). The shipped drops are a first
// vertical over Roaring's company data: look up a company by its organisation
// number (roaring_company_overview) and search for one by name
// (roaring_company_search). Together they're the resolve-then-enrich loop a
// workflow uses to turn a bare org number (or a typed company name) into
// structured data.
//
// Auth is Roaring's OAuth2 *client-credentials* flow: a Consumer Key + Consumer
// Secret are exchanged for a short-lived bearer token at POST /token (HTTP Basic
// with the key:secret, grant_type=client_credentials) —
// https://developer.roaring.io/docs/guides/api-authorization-guide. Because this
// is a two-legged machine grant (no per-user consent, no redirect), it needs NO
// daemon-side OAuth provider or token store: the connector performs the exchange
// itself at run time from the static key/secret and caches the token in memory
// until it nears expiry. So — like 46elks / Klarna / nShift — this is a
// static-credential connector from the platform's point of view.
//
// The key + secret are a per-tenant service connection (Manifest.ConnectionFields),
// set once on the Apps page (stored as conn.roaring.*) and injected into each
// action's job at run time, so credentials never live in the graph.
package roaring

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a hostile or
// buggy upstream (reachable via the base_url override) can't OOM the daemon.
const maxResponseBytes = 8 << 20 // 8 MiB — company reports carry rich records

// defaultBase is Roaring's production API root. Overridable per job via base_url
// (the test seam; also lets an advanced deployment repoint the host).
const defaultBase = "https://api.roaring.io"

// baseURL resolves the API root for one job: an explicit base_url param wins
// (trailing slash trimmed), otherwise the production host.
func baseURL(job core.Job) string {
	if u, _ := params.StringOpt(job.Params, "base_url"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return defaultBase
}

// roaringConnectionFields is the per-tenant Roaring connection: the OAuth2
// client-credentials Consumer Key + Secret (Development → Access keys in the
// Roaring portal), entered once on the Apps page and injected into each action's
// job at run time. Shared by every action drop.
func roaringConnectionFields() []core.ConnectionField {
	return []core.ConnectionField{
		{Key: "client_key", Label: "Consumer Key", Required: true, Placeholder: "from Roaring → Development → Access keys"},
		{Key: "client_secret", Label: "Consumer Secret", Secret: true, Required: true},
	}
}

// resolveCreds reads the client_key + client_secret — the per-tenant Roaring
// connection, injected into the job params at run time by the engine. An empty
// value means the connection hasn't been set up, and the error says so.
func resolveCreds(job core.Job) (key, secret string, err error) {
	key, _ = params.StringOpt(job.Params, "client_key")
	secret, _ = params.StringOpt(job.Params, "client_secret")
	if strings.TrimSpace(key) == "" || strings.TrimSpace(secret) == "" {
		return "", "", fmt.Errorf("Roaring is not connected: add your Consumer Key and Secret on the Apps page (Roaring)")
	}
	return key, secret, nil
}

// --- client-credentials token cache -----------------------------------------

// cachedToken is one memoised bearer token with the instant it should be
// considered stale (the real expiry minus a safety margin).
type cachedToken struct {
	token   string
	expires time.Time
}

// tokenCache memoises client-credentials tokens across job runs so a burst of
// enrichment calls shares one exchange instead of hitting /token every time.
// Keyed by (base, client_key) so distinct connections — and distinct test
// servers — never share a token.
var (
	tokenMu    sync.Mutex
	tokenCache = map[string]cachedToken{}
)

// tokenSafetyMargin is subtracted from the reported lifetime so a token isn't
// used right as it expires (clock skew + in-flight latency).
const tokenSafetyMargin = 60 * time.Second

// resolveToken returns a valid bearer token for the job's connection, exchanging
// the Consumer Key/Secret at /token when the cache is empty or stale.
func resolveToken(ctx context.Context, job core.Job) (string, error) {
	key, secret, err := resolveCreds(job)
	if err != nil {
		return "", err
	}
	base := baseURL(job)
	cacheKey := base + "\x00" + key

	tokenMu.Lock()
	if t, ok := tokenCache[cacheKey]; ok && time.Now().Before(t.expires) {
		tok := t.token
		tokenMu.Unlock()
		return tok, nil
	}
	tokenMu.Unlock()

	tok, ttl, err := fetchToken(ctx, job, base, key, secret)
	if err != nil {
		return "", err
	}
	tokenMu.Lock()
	tokenCache[cacheKey] = cachedToken{token: tok, expires: time.Now().Add(ttl - tokenSafetyMargin)}
	tokenMu.Unlock()
	return tok, nil
}

// fetchToken performs the client-credentials exchange: HTTP Basic with the
// key:secret and a grant_type=client_credentials form body. Returns the token
// and its reported lifetime.
func fetchToken(ctx context.Context, job core.Job, base, key, secret string) (string, time.Duration, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	headers := map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(key+":"+secret)),
		"Content-Type":  "application/x-www-form-urlencoded",
		"Accept":        "application/json",
	}
	// base_url is tenant-supplied, so net.Do guards the dial (SSRF + egress
	// allowlist) — the same protection the data calls get.
	status, body, _, err := hfnet.Do(ctx, http.MethodPost, base+"/token", headers, []byte(form.Encode()), params.TimeoutMS(job, 15000), maxResponseBytes)
	if err != nil {
		return "", 0, fmt.Errorf("Roaring token request failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", 0, fmt.Errorf("Roaring token request rejected (%d) — check your Consumer Key and Secret", status)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
		return "", 0, fmt.Errorf("Roaring token response was not understood")
	}
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= tokenSafetyMargin {
		// Defensive: an absent/absurd expires_in shouldn't yield a token that's
		// already stale. Fall back to a conservative minute.
		ttl = tokenSafetyMargin + time.Minute
	}
	return tr.AccessToken, ttl, nil
}

// --- data calls --------------------------------------------------------------

// roaringGet runs one authenticated GET against a Roaring data endpoint, minting
// or reusing a bearer token first. Returns status + body; the caller maps non-2xx
// via roaringFailure.
func roaringGet(ctx context.Context, job core.Job, endpoint string) (int, []byte, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return 0, nil, err
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        "application/json",
	}
	status, body, _, err := hfnet.Do(ctx, http.MethodGet, endpoint, headers, nil, params.TimeoutMS(job, 15000), maxResponseBytes)
	return status, body, err
}

// extractRoaringError pulls a human message out of a Roaring error body. Roaring
// returns {message} or {error,error_description} shapes; APIErrorMessage handles
// the flat {message} and falls back to the truncated raw body for the rest.
func extractRoaringError(body []byte) string {
	var e struct {
		Message          string `json:"message"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &e); err == nil {
		switch {
		case e.Message != "":
			return e.Message
		case e.ErrorDescription != "":
			return e.ErrorDescription
		case e.Error != "":
			return e.Error
		}
	}
	return params.APIErrorMessage(body, 300)
}

// roaringFailure maps a transport error or a non-2xx Roaring response to an error
// Result, or nil on success — the shared epilogue of every drop's roaringGet
// call. A credential/token failure surfaced by resolveToken arrives here as a
// transport-style error too.
func roaringFailure(job core.Job, status int, body []byte, err error) *core.Result {
	return params.HTTPFailure(job, "roaring", "Roaring", status, body, err, extractRoaringError)
}
