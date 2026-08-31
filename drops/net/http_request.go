// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package net houses modules that reach outside the daemon's host.
// http_request is the workhorse — most real workflows need to call an
// external API at some point.
package net

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	stdnet "net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/mimetype"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/pollstate"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "http_request",
			Version:     "1.0",
			Label:       "Web request",
			Subtitle:    "Call a URL or API",
			Color:       "#5599ee",
			Icon:        "globe",
			Category:    "network",
			Provider:    "internal",
			Integration: "HTTP",
			Tags:        []string{"http", "rest", "api", "webhook"},
			Description: "Call any web address (API) — GET, POST, PUT, PATCH, or DELETE. Useful when the service you want to talk to doesn't have a dedicated step here yet. The response, the status code, and the headers come out on separate ports, so a Branch can test the status directly. Private-network addresses are blocked by default to prevent accidental internal calls.",
			Summary:     "Call a web address (API) and get the response, status code, and headers back as separate ports.",
			Examples: []core.ParamsExample{
				{
					Title:  "Simple authenticated GET",
					Params: json.RawMessage(`{"url":"https://api.example.com/v1/users","method":"GET","headers":{"Authorization":"Bearer ${secret.EXAMPLE_API_TOKEN}","Accept":"application/json"}}`),
				},
				{
					Title:  "POST JSON payload, accept 201",
					Params: json.RawMessage(`{"url":"https://api.example.com/v1/orders","method":"POST","headers":{"Content-Type":"application/json","Authorization":"Bearer ${secret.EXAMPLE_API_TOKEN}"},"body":"{\"sku\":\"ABC-123\",\"qty\":2}","expect_status":[200,201]}`),
				},
				{
					Title:  "DELETE with explicit status expectation and short timeout",
					Params: json.RawMessage(`{"url":"https://api.example.com/v1/sessions/42","method":"DELETE","timeout_ms":5000,"expect_status":[204]}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// url is both a param and an input port (same id) so it can be
				// typed inline on the pin OR wired from an upstream node that
				// builds the target URL. Listed first — it's the primary input.
				// Typed text/plain so it reads as a string pin (green) and wires
				// cleanly from a Text source.
				{Port: "url", Label: "URL", MIME: []string{"text/plain"}},
				{Port: "request_body", Label: "Body"},
			},
			Outputs: []core.Port{
				// Status first: it's the field most flows branch on, so it
				// sits at the top of the output column. Status and headers are
				// separate ports (not one meta blob) so a downstream Branch can
				// test the numeric status code directly — wire status →
				// Compare with in_range [200,299] to fork on success, e.g.
				{Port: "status", Label: "Status", MIME: []string{"application/json"}},
				{Port: "response_body", Label: "Response"},
				{Port: "headers", Label: "Headers", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"url":{"type":"string","title":"URL","description":"The web address to call. The URL input overrides this when connected."},
						"method":{"type":"string","title":"Method","default":"GET","enum":["GET","POST","PUT","PATCH","DELETE","HEAD","OPTIONS"],"description":"What kind of request to make. GET fetches data; POST/PUT/PATCH send the Body along."},
						"body":{"type":"string","title":"Body","description":"Text to send with the request (POST/PUT/PATCH). The Body input overrides this when connected."},
						"headers":{"type":"object","title":"Headers","additionalProperties":{"type":"string"},"description":"Extra request headers (one per key). Values may include ${secret.NAME} placeholders that resolve to stored secrets."},
						"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for the full request, in milliseconds."},
						"expect_status":{"type":"array","title":"Accepted status codes","items":{"type":"integer"},"x_advanced":true,"description":"Status codes treated as success. Empty defaults to 2xx."},
						"max_body_bytes":{"type":"integer","title":"Max response bytes","default":10485760,"minimum":0,"x_advanced":true,"description":"Fail responses larger than this. Default 10 MiB."},
						"cache_key":{"type":"string","title":"Cache key","x_advanced":true,"description":"Set this on a GET you poll repeatedly to skip re-downloading unchanged data: the step remembers the server's ETag/Last-Modified and sends them next time. When nothing changed the server replies fast with status 304 and an empty Response — branch on status to skip work. Use a unique name per polled URL."},
						"allow_private_networks":{"type":"boolean","title":"Allow private networks","default":false,"x_advanced":true,"description":"Disable the private-address guard. Only enable when calling a local service intentionally."}
					},
					"required":["url"]
				}`,
			),
			// idempotent=true so retry edges validate. GET/HEAD/OPTIONS/
			// PUT/DELETE are safe to replay; POST/PATCH are not idempotent
			// under HTTP, so for those we attach a stable Idempotency-Key
			// (see executeHTTPRequest) to let compliant services dedupe a
			// retried-but-already-applied write. Note the worker only
			// auto-retries (without an explicit on_error=retry edge) on
			// idempotent leaf nodes — so a leaf POST relies on that key.
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeHTTPRequest,
	})
}

const (
	defaultTimeoutMs    = 30000
	defaultMaxBodyBytes = 10 * 1024 * 1024 // 10 MiB
	// maxMaxBodyBytes is the hard ceiling on the author-settable
	// max_body_bytes. http_request buffers the whole response in memory
	// (io.ReadAll below), so without a cap an author could set max_body_bytes
	// to gigabytes, point at a large/attacker-controlled URL, and OOM the
	// shared daemon. Large transfers should use http_download, which streams
	// to disk under the workspace quota.
	maxMaxBodyBytes = 100 * 1024 * 1024 // 100 MiB
)

func executeHTTPRequest(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	url := resolveURL(job)
	if strings.TrimSpace(url) == "" {
		return params.Err(job, "bad_param", "url is required: connect the URL input or set the url param"), nil
	}
	// Operator egress allowlist (above the IP-level SSRF guard). No-op
	// when no allowlist is configured.
	if err := EgressAllowedFor(ctx, url); err != nil {
		return params.Err(job, "egress_blocked", err.Error()), nil
	}

	method := params.StringDefault(job.Params, "method", "GET")
	method = strings.ToUpper(method)
	timeoutMs := params.IntDefault(job.Params, "timeout_ms", defaultTimeoutMs)
	maxBodyBytes := int64(params.IntDefault(job.Params, "max_body_bytes", defaultMaxBodyBytes))
	if maxBodyBytes > maxMaxBodyBytes {
		maxBodyBytes = maxMaxBodyBytes
	}
	// allow_private_networks disables the SSRF guard; only honor it when the
	// operator has opted in (DAZYFLOW_ALLOW_PRIVATE_EGRESS). Otherwise it's a
	// tenant-controllable SSRF bypass to metadata/localhost/internal hosts.
	reqAllowPrivate, _ := params.Bool(job.Params, "allow_private_networks")
	allowPrivate := reqAllowPrivate && PrivateEgressAllowed()

	headers, err := paramHeaders(job.Params, "headers")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	bodyReader, err := buildRequestBody(job)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}

	expectStatus := params.IntSlice(job.Params, "expect_status")

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return params.Err(job, "bad_url", err.Error()), nil
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// For verbs that are not idempotent under HTTP semantics (POST/PATCH),
	// attach a stable Idempotency-Key so that a retry of a request whose
	// response was lost dedupes on any service that honors the convention
	// (Stripe, GitHub, most modern REST APIs). The key is constant across
	// retries of the same node record; services that ignore the header are
	// unaffected. Mirrors webhook_send. Don't override a user-set key.
	if method == http.MethodPost || method == http.MethodPatch {
		if req.Header.Get("Idempotency-Key") == "" {
			req.Header.Set("Idempotency-Key", job.IdempotencyKey())
		}
	}

	// Conditional-request caching (opt-in via cache_key). For safe re-fetches
	// (GET/HEAD) we send the validators the server last gave us so it can
	// answer 304 Not Modified with no body — the dominant cost saver for
	// polling. Only active when a store is wired (cmd/dzd); off in tests.
	cacheKey := strings.TrimSpace(params.StringDefault(job.Params, "cache_key", ""))
	conditional := cacheKey != "" && httpCacheEnabled() && (method == http.MethodGet || method == http.MethodHead)
	cacheName := httpCacheName(job.GraphID, job.NodeID, cacheKey)
	// sentConditional is true only once we've actually attached an
	// If-None-Match/If-Modified-Since — i.e. there were stored validators to
	// send. A 304 is only meaningful in answer to such a header, so this gates
	// the 304 fast-path below (an unsolicited 304 — e.g. on the very first poll
	// with nothing stored — must fall through to normal handling).
	sentConditional := false
	if conditional {
		sentConditional = applyConditionalHeaders(req, readCacheValidators(ctx, job.Tenant, cacheName))
	}

	params.EmitProgress(progress, job, 0.1, fmt.Sprintf("%s %s", method, url))

	// Per-(tenant, host) pacing before dialing: bound rate + concurrency and
	// wait out any prior 429 cooldown for this host so one tenant's burst
	// can't exhaust a shared third-party API budget or the egress IP's
	// reputation. Honors ctx (node timeout / cancel).
	release, lerr := AcquireEgress(ctx, url)
	if lerr != nil {
		return params.Err(job, "cancelled", lerr.Error()), lerr
	}
	defer release()

	client := buildClient(time.Duration(timeoutMs)*time.Millisecond, allowPrivate)
	resp, err := client.Do(req)
	if err != nil {
		if isSSRFError(err) {
			return params.Err(job, "ssrf_blocked", err.Error()), nil
		}
		if strings.Contains(err.Error(), "egress_blocked") {
			return params.Err(job, "egress_blocked", err.Error()), nil
		}
		if ctx.Err() != nil {
			return params.Err(job, "cancelled", ctx.Err().Error()), ctx.Err()
		}
		return params.Err(job, "http", err.Error()), nil
	}
	defer resp.Body.Close()
	// Record rate-limit signals so the next call to this host self-paces and
	// a 429 lengthens the worker's retry backoff to the server-asked interval.
	ObserveEgressResponse(ctx, url, resp.StatusCode, resp.Header)

	params.EmitProgress(progress, job, 0.7, fmt.Sprintf("received %d", resp.StatusCode))

	// 304 Not Modified (only reachable when we sent conditional headers): the
	// resource is unchanged, so there's no body to read — emit an explicit
	// "not modified" result a downstream Branch can skip on, and tell the
	// scheduler this poll was empty so it can widen the interval. Validators
	// are unchanged, so nothing to re-store.
	if sentConditional && resp.StatusCode == http.StatusNotModified {
		pollstate.Report(ctx, job, false)
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: map[string]core.Ref{
				"response_body": {MIME: "application/json", Inline: nil},
				"status":        {MIME: "application/json", Inline: http.StatusNotModified},
				"headers":       {MIME: "application/json", Inline: flattenHeaders(resp.Header)},
			},
		}, nil
	}

	if !statusAccepted(resp.StatusCode, expectStatus) {
		// Fold a bounded snippet of the response body into the error. APIs
		// almost always explain a 4xx/5xx there (which param is missing, why
		// auth failed, a rate-limit note); without it the error is just
		// "got 422" with no way to tell why from the inspector.
		msg := fmt.Sprintf("got %d, expected %s", resp.StatusCode, formatExpectStatus(expectStatus))
		if snippet := readErrorSnippet(resp.Body); snippet != "" {
			msg += ": " + snippet
		}
		return params.Err(job, "unexpected_status", msg), nil
	}

	// A fresh, accepted response: remember its validators so the next fetch
	// can be conditional, and mark the poll active (data delivered) so the
	// scheduler keeps the base cadence.
	if conditional {
		v := validatorsFromResponse(resp.Header)
		switch {
		case v.ETag != "" || v.LastModified != "":
			writeCacheValidators(ctx, job.Tenant, cacheName, v)
		case sentConditional:
			// We sent a validator but this fresh response carries none — the
			// upstream dropped it. Clear the stale one so we stop conditioning
			// on a validator the server no longer recognises. (When we sent
			// nothing, there's nothing stored to clear.)
			clearCacheValidators(ctx, job.Tenant, cacheName)
		}
		pollstate.Report(ctx, job, true)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return params.Err(job, "cancelled", ctx.Err().Error()), ctx.Err()
		}
		return params.Err(job, "io", fmt.Sprintf("read body: %v", err)), nil
	}
	if int64(len(raw)) > maxBodyBytes {
		return params.Err(job, "body_too_large",
			fmt.Sprintf("response exceeds %d bytes", maxBodyBytes)), nil
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	var bodyInline any
	if mimetype.IsText(contentType) {
		bodyInline = string(raw)
	} else {
		bodyInline = raw
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"response_body": {MIME: contentType, Inline: bodyInline},
			// status is emitted as a bare JSON number so a Branch's
			// numeric comparison (greater_than/greater_or_equal/…) can
			// test it without any parse step in between.
			"status":  {MIME: "application/json", Inline: resp.StatusCode},
			"headers": {MIME: "application/json", Inline: flattenHeaders(resp.Header)},
		},
	}, nil
}

// resolveURL takes the target from the wired `url` input when it carries a
// non-empty string, else falls back to the url param — so the URL can be a
// literal in the graph or computed by an upstream node.
func resolveURL(job core.Job) string {
	if ref, ok := job.Input["url"]; ok {
		if s, ok := ref.Inline.(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return params.StringDefault(job.Params, "url", "")
}

// buildRequestBody honors the input port first (so POST bodies can be
// piped from a previous node), and falls back to params.body for
// inline-literal bodies in the graph definition.
func buildRequestBody(job core.Job) (io.Reader, error) {
	if input, ok := job.Input["request_body"]; ok {
		switch v := input.Inline.(type) {
		case string:
			return strings.NewReader(v), nil
		case []byte:
			return bytes.NewReader(v), nil
		case nil:
			// fall through to params
		default:
			b, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("marshal request_body: %w", err)
			}
			return bytes.NewReader(b), nil
		}
	}
	if s, ok := job.Params["body"].(string); ok && s != "" {
		return strings.NewReader(s), nil
	}
	return nil, nil
}

func statusAccepted(got int, expect []int) bool {
	if len(expect) == 0 {
		return got >= 200 && got < 300
	}
	for _, e := range expect {
		if got == e {
			return true
		}
	}
	return false
}

func formatExpectStatus(expect []int) string {
	if len(expect) == 0 {
		return "2xx"
	}
	parts := make([]string, len(expect))
	for i, e := range expect {
		parts[i] = fmt.Sprintf("%d", e)
	}
	return strings.Join(parts, ",")
}

// errorSnippetBytes bounds how much of a failed response body we fold into the
// error message — enough to carry an API's validation detail, not so much it
// floods the run log or leaks a large payload.
const errorSnippetBytes = 512

// readErrorSnippet reads a short, single-line snippet of a failed response
// body for the error message. Best-effort: whitespace is collapsed to keep the
// message on one line, and the result is truncated with an ellipsis. Returns
// "" when the body is empty or unreadable.
func readErrorSnippet(body io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(body, errorSnippetBytes+1))
	s := strings.Join(strings.Fields(string(raw)), " ")
	if len(s) > errorSnippetBytes {
		s = s[:errorSnippetBytes] + "…"
	}
	return s
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		if len(vs) > 0 {
			out[k] = vs[0]
		}
	}
	return out
}

// SafeHTTPClient returns an http.Client whose dialer blocks
// private/loopback/link-local destinations (the SSRF guard) unless
// allowPrivate is true. Exported so other drops that dial
// user-influenced URLs (notify/webhook_send) get the same protection
// instead of a bare http.Client. SSRF rejections surface as a dial
// error whose message contains "ssrf_blocked" (see IsSSRFError).
func SafeHTTPClient(timeout time.Duration, allowPrivate bool) *http.Client {
	// Clients are cached per (timeout, allowPrivate): a fresh client per
	// call would rebuild its Transport each time, so the idle-connection
	// pool never got reused and every connector API call paid a new
	// TCP+TLS handshake. There are only a handful of distinct timeouts
	// across the drops, so the cache stays tiny.
	key := clientKey{timeout: timeout, allowPrivate: allowPrivate}
	if c, ok := clientCache.Load(key); ok {
		return c.(*http.Client)
	}
	c, _ := clientCache.LoadOrStore(key, buildClient(timeout, allowPrivate))
	return c.(*http.Client)
}

type clientKey struct {
	timeout      time.Duration
	allowPrivate bool
}

var clientCache sync.Map

// IsSSRFError reports whether a client.Do error came from the SSRF
// dial guard, so callers can map it to a friendly error code.
func IsSSRFError(err error) bool { return isSSRFError(err) }

// buildClient configures the http.Client with the SSRF guard installed
// at dial time. The Control hook fires on each TCP connection attempt
// after DNS resolution — so even hostnames that resolve to private IPs
// are blocked.
func buildClient(timeout time.Duration, allowPrivate bool) *http.Client {
	dialer := &stdnet.Dialer{Timeout: timeout}
	if !allowPrivate {
		dialer.Control = func(network, address string, _ syscall.RawConn) error {
			return ssrfGuard(address)
		}
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
		},
		// The operator egress allowlist is enforced on the initial URL by
		// the caller, but the Go default redirect policy would happily
		// follow a 30x to any other host — bypassing the allowlist. Re-run
		// the host check on every hop so a redirect can't be used to reach
		// a host the operator didn't permit. (EgressAllowed is a no-op when
		// no allowlist is configured, so this changes nothing by default.
		// Private/loopback IPs are independently blocked by the dial guard.)
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return EgressAllowedFor(req.Context(), req.URL.String())
		},
	}
}

// SSRFDialControl returns a net.Dialer Control hook that blocks dialing
// loopback/private/link-local addresses. Because Control runs after DNS
// resolution on the resolved IP, it resists DNS rebinding. Returns nil when
// the operator has opted into private egress (no restriction). Reusable by
// non-HTTP drops that dial user-supplied hosts (Postgres, SMTP, …) so they
// share the one SSRF policy instead of dialing arbitrary internal hosts.
func SSRFDialControl() func(network, address string, c syscall.RawConn) error {
	if PrivateEgressAllowed() {
		return nil
	}
	return func(_, address string, _ syscall.RawConn) error {
		return ssrfGuard(address)
	}
}

// CheckDialHost is a pre-flight (pre-dial) SSRF check for a "host:port" or
// bare host, for drivers that don't expose a dial hook (e.g. database/sql
// MySQL). It resolves the host and refuses if any resolved IP is
// loopback/private/link-local — unless the operator opted into private
// egress. Weaker than SSRFDialControl against rebinding, but blocks the
// common "point me at an internal host" case. nil = allowed.
func CheckDialHost(hostPort string) error {
	if PrivateEgressAllowed() {
		return nil
	}
	host := hostPort
	if h, _, err := stdnet.SplitHostPort(hostPort); err == nil {
		host = h
	}
	if ip := stdnet.ParseIP(host); ip != nil {
		if isUnsafeIP(ip) {
			return fmt.Errorf("ssrf_blocked: %s is loopback/private/link-local", ip)
		}
		return nil
	}
	ips, err := stdnet.LookupIP(host)
	if err != nil {
		return fmt.Errorf("ssrf_blocked: cannot resolve %q", host)
	}
	for _, ip := range ips {
		if isUnsafeIP(ip) {
			return fmt.Errorf("ssrf_blocked: %s resolves to loopback/private/link-local", host)
		}
	}
	return nil
}

func ssrfGuard(address string) error {
	host, _, err := stdnet.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("ssrf_blocked: cannot parse %q", address)
	}
	ip := stdnet.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("ssrf_blocked: %q is not an IP", host)
	}
	if isUnsafeIP(ip) {
		return fmt.Errorf("ssrf_blocked: %s is loopback/private/link-local", ip)
	}
	return nil
}

// extraUnsafeCIDRs are internal-routed ranges that Go's IP predicates don't
// classify: RFC 6598 carrier-grade NAT (100.64.0.0/10, routed inside many
// hosting providers) and RFC 6052 NAT64 (64:ff9b::/96, which can embed an
// IPv4 metadata/private address inside an IPv6 literal).
var extraUnsafeCIDRs = func() []*stdnet.IPNet {
	var out []*stdnet.IPNet
	for _, c := range []string{"100.64.0.0/10", "64:ff9b::/96"} {
		if _, n, err := stdnet.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// isUnsafeIP enumerates the address ranges that should never be reachable
// from a user-supplied URL. Loopback (127/8, ::1), link-local (169.254/16
// — AWS metadata!), RFC 1918 (10/8, 172.16/12, 192.168/16), RFC 4193
// (fc00::/7), CGNAT (100.64/10), NAT64 (64:ff9b::/96), multicast, and
// unspecified all get blocked.
func isUnsafeIP(ip stdnet.IP) bool {
	if ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() {
		return true
	}
	for _, n := range extraUnsafeCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func isSSRFError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "ssrf_blocked")
}
