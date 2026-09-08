// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
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
				{Port: "url", Label: "URL", MIME: []string{"text/plain"}},
				{Port: "request_body", Label: "Body"},
			},
			Outputs: []core.Port{
				// Status first: it is the field most flows branch on.
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
			// GET/HEAD/OPTIONS are idempotent per HTTP, so a retry edge validates.
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeHTTPRequest,
	})
}

const (
	defaultTimeoutMs    = 30000
	defaultMaxBodyBytes = 10 * 1024 * 1024 // 10 MiB
	// The ceiling an author cannot raise.
	maxMaxBodyBytes = 100 * 1024 * 1024 // 100 MiB
)

func executeHTTPRequest(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	url := resolveURL(job)
	if strings.TrimSpace(url) == "" {
		return params.Err(job, "bad_param", "url is required: connect the URL input or set the url param"), nil
	}
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
	// Disables the SSRF guard, so it is honoured only if the operator opted in.
	reqAllowPrivate, _ := params.Bool(job.Params, "allow_private_networks")
	allowPrivate := reqAllowPrivate && PrivateEgressAllowed()

	headers, err := paramHeaders(job.Params, "headers")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	bodyReader, err := params.RequestBody(job)
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

	// A stable Idempotency-Key, so a retry whose response was lost dedupes.
	if method == http.MethodPost || method == http.MethodPatch {
		if req.Header.Get("Idempotency-Key") == "" {
			req.Header.Set("Idempotency-Key", job.IdempotencyKey())
		}
	}

	cacheKey := strings.TrimSpace(params.StringDefault(job.Params, "cache_key", ""))
	conditional := cacheKey != "" && httpCacheEnabled() && (method == http.MethodGet || method == http.MethodHead)
	cacheName := httpCacheName(job.GraphID, job.NodeID, cacheKey)
	// Only once a validator was actually attached.
	sentConditional := false
	if conditional {
		sentConditional = applyConditionalHeaders(req, readCacheValidators(ctx, job.Tenant, cacheName))
	}

	params.EmitProgress(progress, job, 0.1, fmt.Sprintf("%s %s", method, url))

	// Paced before dialing, so a flow cannot hammer one host.
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
	ObserveEgressResponse(ctx, url, resp.StatusCode, resp.Header)

	params.EmitProgress(progress, job, 0.7, fmt.Sprintf("received %d", resp.StatusCode))

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

	if !params.StatusAccepted(resp.StatusCode, expectStatus) {
		// Bounded: APIs explain themselves in the body, but not in a megabyte of it.
		msg := fmt.Sprintf("got %d, expected %s", resp.StatusCode, formatExpectStatus(expectStatus))
		if snippet := readErrorSnippet(resp.Body); snippet != "" {
			msg += ": " + snippet
		}
		return params.Err(job, "unexpected_status", msg), nil
	}

	if conditional {
		v := validatorsFromResponse(resp.Header)
		switch {
		case v.ETag != "" || v.LastModified != "":
			writeCacheValidators(ctx, job.Tenant, cacheName, v)
		case sentConditional:
			// A fresh response with no validator means the cache entry is unusable.
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
			"status":        {MIME: "application/json", Inline: resp.StatusCode},
			"headers":       {MIME: "application/json", Inline: flattenHeaders(resp.Header)},
		},
	}, nil
}

func resolveURL(job core.Job) string {
	if ref, ok := job.Input["url"]; ok {
		if s, ok := ref.Inline.(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return params.StringDefault(job.Params, "url", "")
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

const errorSnippetBytes = 512

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

func SafeHTTPClient(timeout time.Duration, allowPrivate bool) *http.Client {
	// A fresh client per request leaks connections and defeats keep-alive.
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

func IsSSRFError(err error) bool { return isSSRFError(err) }

// The guard is installed at DIAL time, after DNS resolution.
func buildClient(timeout time.Duration, allowPrivate bool) *http.Client {
	dialer := &stdnet.Dialer{Timeout: timeout}
	if !allowPrivate {
		dialer.Control = func(network, address string, _ syscall.RawConn) error {
			return ssrfGuard(address)
		}
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &triggerDepthTransport{base: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
		}},
		// Enforced on the initial URL AND on every redirect hop.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return EgressAllowedFor(req.Context(), req.URL.String())
		},
	}
}

// Fires per connection attempt AFTER DNS resolution, so a hostname that resolves
// to a private address is refused just as a literal one is.
func SSRFDialControl() func(network, address string, c syscall.RawConn) error {
	if PrivateEgressAllowed() {
		return nil
	}
	return func(_, address string, _ syscall.RawConn) error {
		return ssrfGuard(address)
	}
}

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

// Ranges Go's own IP predicates do not classify as private.
var extraUnsafeCIDRs = func() []*stdnet.IPNet {
	var out []*stdnet.IPNet
	for _, c := range []string{"100.64.0.0/10", "64:ff9b::/96"} {
		if _, n, err := stdnet.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// Never reachable from a tenant-supplied URL.
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
