// Package gcal hosts the native Google Calendar connectors (gcal_list_events,
// gcal_create_event). They authenticate with Google OAuth (the "google"
// provider) via the SetTokenLookup hook the daemon wires at startup — the same
// provider and token plumbing the gmail and sheets packages use, so connecting
// a Google account for Calendar tops up the existing grant incrementally.
package gcal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a hostile or
// buggy upstream can't OOM the daemon by streaming an unbounded body.
const maxResponseBytes = 16 << 20 // 16 MiB

const calendarAPIBase = "https://www.googleapis.com/calendar/v3"

type TokenLookup func(ctx context.Context, account string) (string, error)

var (
	tokenLookupMu sync.RWMutex
	tokenLookup   TokenLookup
)

// SetTokenLookup wires the daemon's per-account Google token resolver. Mirrors
// gmail.SetTokenLookup / sheets.SetTokenLookup — bound to the "google" provider.
func SetTokenLookup(fn TokenLookup) {
	tokenLookupMu.Lock()
	defer tokenLookupMu.Unlock()
	tokenLookup = fn
}

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	// `token` is not a user-facing param, but the engine honors it when present:
	// it's the injection seam the integration tests use to stand in for a
	// connected account. The UI path always goes through the account lookup.
	if t, _ := params.StringOpt(job.Params, "token"); t != "" {
		return t, nil
	}
	account, _ := params.StringOpt(job.Params, "account")
	if account == "" {
		account = "default"
	}
	tokenLookupMu.RLock()
	fn := tokenLookup
	tokenLookupMu.RUnlock()
	if fn == nil {
		return "", fmt.Errorf("no Google token: connect a Google account via /api/v1/oauth/google/authorize")
	}
	tok, err := fn(ctx, account)
	if err != nil {
		return "", fmt.Errorf("lookup token for account %q: %w", account, err)
	}
	if tok == "" {
		return "", fmt.Errorf("google account %q is not connected", account)
	}
	return tok, nil
}

// Test seam: tests point the API root at one httptest server.
var (
	baseMu  sync.RWMutex
	calBase = calendarAPIBase
)

// SetHTTPBase swaps the Calendar API root (tests point it at an httptest server).
func SetHTTPBase(base string) {
	baseMu.Lock()
	defer baseMu.Unlock()
	calBase = base
}

// base_url is not a user-facing param, but like `token` the engine honors it
// when present — the integration tests point it at an httptest server. The
// SafeHTTPClient + egress guard in googleDo still bound where the bearer token
// may be sent.
func calBaseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "base_url"); b != "" {
		return b
	}
	baseMu.RLock()
	defer baseMu.RUnlock()
	return calBase
}

func googleDo(ctx context.Context, method, url, token, contentType string, body []byte, timeoutMS int) (int, []byte, error) {
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// Guard the dial: base_url can still arrive via the API/test params, so the
	// SSRF client blocks loopback/private/link-local targets and the egress
	// allowlist (when set) bounds which public hosts the bearer token may reach.
	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
		return 0, nil, err
	}
	resp, err := hfnet.SafeHTTPClient(time.Duration(timeoutMS)*time.Millisecond, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if int64(len(raw)) > maxResponseBytes {
		return resp.StatusCode, nil, fmt.Errorf("google response exceeds %d bytes", maxResponseBytes)
	}
	return resp.StatusCode, raw, nil
}

// calErr pulls the human message out of a Google API error envelope, falling
// back to a bounded slice of the raw body.
func calErr(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	if len(body) > 512 {
		return string(body[:512])
	}
	return string(body)
}

// calendarID returns the configured calendar id, defaulting to "primary" (the
// connected account's own calendar) when blank.
func calendarID(job core.Job) string {
	id := params.StringDefault(job.Params, "calendar_id", "")
	if id == "" {
		return "primary"
	}
	return id
}
