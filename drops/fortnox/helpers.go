// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package fortnox hosts the native Fortnox connectors — Sweden's dominant SMB
// accounting/invoicing platform. The shipped drops are the first vertical:
// customers (create + a picker) and invoices (create + a paid-invoice poll
// source). Auth is Fortnox OAuth2: the daemon owns the token (its provider
// entry uses client_secret_basic, which Fortnox's token endpoint requires),
// and this package resolves it per-job via the oauthtok hook that the other
// OAuth connectors share.
//
// Fortnox wraps every request and response body in a singular PascalCase key
// ({"Customer": {…}}, {"Invoice": {…}}) — the helpers here marshal/unmarshal
// through that envelope so the drops deal in flat structs.
//
// Fortnox has no webhooks, so event reactions ("new paid invoice") compose the
// same way the Stripe/Gmail connectors document: poll_trigger →
// fortnox_list_invoices (cursor in `page`) → for_each.
package fortnox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/apibase"
	"git.sr.ht/~klahr/dazyflow/drops/internal/oauthtok"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a hostile or
// buggy upstream (reachable via the base_url override) can't OOM the daemon by
// streaming an unbounded body.
const maxResponseBytes = 16 << 20 // 16 MiB

// tokenHook holds the daemon's per-account Fortnox OAuth lookup plus the
// resolve sequence shared with the other OAuth connectors (see
// drops/internal/oauthtok). display/slug/noun feed its "not connected" errors.
var tokenHook = oauthtok.New("Fortnox", "fortnox", "Fortnox")

// SetTokenLookup wires (or clears) the daemon's token lookup. Called once at
// dzd startup (cmd/dzd/main.go, bound to the "fortnox" OAuth provider).
func SetTokenLookup(fn oauthtok.Lookup) { tokenHook.Set(fn) }

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	return tokenHook.Resolve(ctx, job)
}

var httpBase = apibase.New("https://api.fortnox.se/3")

// SetHTTPBase swaps the Fortnox API root (tests point it at httptest).
func SetHTTPBase(base string) { httpBase.Set(base) }

func baseURL(job core.Job) string { return httpBase.For(job) }

// fortnoxDo runs one authenticated Fortnox API call. `body` nil means no
// request body (GETs). Returns status + raw body; the caller maps non-2xx via
// fortnoxFailure. token is passed in (not resolved here) so tests can inject a
// value and the SSRF-guard test can call this directly.
func fortnoxDo(ctx context.Context, method, url, token string, body []byte, timeoutMS int) (int, []byte, error) {
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        "application/json",
	}
	if body != nil {
		headers["Content-Type"] = "application/json"
	}
	// base_url is a tenant-supplied param, so net.Do guards the dial: the SSRF
	// client blocks loopback/private/link-local targets and the egress allowlist
	// (when set) bounds which public hosts the bearer token may be sent to.
	status, raw, _, err := hfnet.Do(ctx, method, url, headers, body, timeoutMS, maxResponseBytes)
	return status, raw, err
}

// call is the shared prologue for the drops: resolve the token, run the
// request, and hand back status/body. Keeps each Execute free of auth
// boilerplate.
func call(ctx context.Context, job core.Job, method, path string, body []byte) (int, []byte, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return 0, nil, err
	}
	return fortnoxDo(ctx, method, baseURL(job)+path, token, body, params.TimeoutMS(job, 15000))
}

// extractFortnoxError pulls the human message out of Fortnox's error envelope
// — {"ErrorInformation":{"Error":1,"Message":"…","Code":2000434}} — so the
// real reason ("Kunden kunde inte hittas") reaches the user instead of a bare
// HTTP status.
func extractFortnoxError(body []byte) string {
	var e struct {
		ErrorInformation struct {
			Message string `json:"Message"`
			Code    int    `json:"Code"`
		} `json:"ErrorInformation"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.ErrorInformation.Message != "" {
		if e.ErrorInformation.Code != 0 {
			return fmt.Sprintf("%d: %s", e.ErrorInformation.Code, e.ErrorInformation.Message)
		}
		return e.ErrorInformation.Message
	}
	if len(body) > 200 {
		return string(body[:200])
	}
	return string(body)
}

// fortnoxFailure maps a transport error or a non-2xx Fortnox response to an
// error Result, or returns nil when the call succeeded — the shared epilogue of
// every drop's call(). Delegates to params.HTTPFailure (the shared
// transport-error/non-2xx epilogue) with Fortnox's error extractor.
func fortnoxFailure(job core.Job, status int, body []byte, err error) *core.Result {
	return params.HTTPFailure(job, "fortnox", "Fortnox", status, body, err, extractFortnoxError)
}

// ListCustomers enumerates the connected account's customers as picker options
// — the backend for the "fortnox-customer" param format, so a user picks
// "Acme AB — 1234" from a dropdown instead of pasting a customer number.
// Called by the daemon's resource-picker endpoint (not by a drop), with the
// account already in the job params.
func ListCustomers(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	status, body, err := call(ctx, job, http.MethodGet, "/customers", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("fortnox returned %d: %s", status, extractFortnoxError(body))
	}
	var parsed struct {
		Customers []struct {
			CustomerNumber string `json:"CustomerNumber"`
			Name           string `json:"Name"`
			Email          string `json:"Email"`
		} `json:"Customers"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse customers: %w", err)
	}
	out := make([]core.AccountResource, 0, len(parsed.Customers))
	for _, c := range parsed.Customers {
		// Label with the name, then the customer number that identifies it;
		// fall back to the number alone for an unnamed customer.
		name := c.Name
		if c.CustomerNumber != "" {
			if name != "" {
				name += " — " + c.CustomerNumber
			} else {
				name = c.CustomerNumber
			}
		}
		if name == "" {
			name = c.CustomerNumber
		}
		out = append(out, core.AccountResource{ID: c.CustomerNumber, Name: name})
	}
	return out, nil
}
