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
	"syscall"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "http_request",
			Version:        "1.0",
			Label:          "HTTP request",
			Color:          "#5599ee",
			Icon:           "globe",
			Category:       "network",
			Provider:       "internal",
			Integration:    "HTTP",
			Tags:           []string{"http", "rest", "api", "webhook"},
			Description:    "Make an HTTP request (GET/POST/PUT/PATCH/DELETE) to any URL. Defaults block private-network addresses to prevent SSRF; the allow_private_networks param opts in for development. Returns the response body and status/header metadata on separate ports.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{{
				Port:  "request_body",
				Label: "Request body (optional, overrides params.body)",
			}},
			Outputs: []core.Port{
				{Port: "response_body", Label: "Response body"},
				{Port: "response_meta", Label: "Status + headers (JSON)"},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"url":{"type":"string","description":"Absolute URL of the resource to call."},
						"method":{"type":"string","default":"GET","enum":["GET","POST","PUT","PATCH","DELETE","HEAD","OPTIONS"],"description":"HTTP verb. Methods with bodies (POST/PUT/PATCH) use the request_body input or the body param."},
						"headers":{"type":"object","additionalProperties":{"type":"string"},"description":"Headers to send (one per key). Values may include ${env:NAME} placeholders that resolve to secrets."},
						"body":{"type":"string","description":"Inline request body. The request_body input port overrides this when connected."},
						"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for the full request, in milliseconds."},
						"expect_status":{"type":"array","items":{"type":"integer"},"description":"Accepted response status codes. Empty defaults to 2xx."},
						"max_body_bytes":{"type":"integer","default":10485760,"minimum":0,"description":"Truncate responses larger than this. Default 10 MiB."},
						"allow_private_networks":{"type":"boolean","default":false,"description":"Disable the SSRF guard. Only enable when calling a local service intentionally."}
					},
					"required":["url"]
				}`,
			),
			// idempotent=true so retry edges validate; users are
			// responsible for restricting the method to one that is
			// actually safe to replay (GET/HEAD/OPTIONS/PUT/DELETE).
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeHTTPRequest,
	})
}

const (
	defaultTimeoutMs    = 30000
	defaultMaxBodyBytes = 10 * 1024 * 1024 // 10 MiB
)

func executeHTTPRequest(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	url, err := paramString(job.Params, "url")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	if strings.TrimSpace(url) == "" {
		return errResult(job, "bad_param", "url is required"), nil
	}

	method := paramStringDefault(job.Params, "method", "GET")
	method = strings.ToUpper(method)
	timeoutMs := paramIntDefault(job.Params, "timeout_ms", defaultTimeoutMs)
	maxBodyBytes := int64(paramIntDefault(job.Params, "max_body_bytes", defaultMaxBodyBytes))
	allowPrivate, _ := paramBool(job.Params, "allow_private_networks")

	headers, err := paramHeaders(job.Params, "headers")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}

	bodyReader, err := buildRequestBody(job)
	if err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}

	expectStatus := paramIntSlice(job.Params, "expect_status")

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return errResult(job, "bad_url", err.Error()), nil
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	emitProgress(progress, job, 0.1, fmt.Sprintf("%s %s", method, url))

	client := buildClient(time.Duration(timeoutMs)*time.Millisecond, allowPrivate)
	resp, err := client.Do(req)
	if err != nil {
		if isSSRFError(err) {
			return errResult(job, "ssrf_blocked", err.Error()), nil
		}
		if ctx.Err() != nil {
			return errResult(job, "cancelled", ctx.Err().Error()), ctx.Err()
		}
		return errResult(job, "http", err.Error()), nil
	}
	defer resp.Body.Close()

	emitProgress(progress, job, 0.7, fmt.Sprintf("received %d", resp.StatusCode))

	if !statusAccepted(resp.StatusCode, expectStatus) {
		return errResult(job, "unexpected_status",
			fmt.Sprintf("got %d, expected %s", resp.StatusCode, formatExpectStatus(expectStatus))), nil
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return errResult(job, "cancelled", ctx.Err().Error()), ctx.Err()
		}
		return errResult(job, "io", fmt.Sprintf("read body: %v", err)), nil
	}
	if int64(len(raw)) > maxBodyBytes {
		return errResult(job, "body_too_large",
			fmt.Sprintf("response exceeds %d bytes", maxBodyBytes)), nil
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	var bodyInline any
	if isTextMIME(contentType) {
		bodyInline = string(raw)
	} else {
		bodyInline = raw
	}

	meta := map[string]any{
		"status":  resp.StatusCode,
		"headers": flattenHeaders(resp.Header),
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"response_body": {MIME: contentType, Inline: bodyInline},
			"response_meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
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

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		if len(vs) > 0 {
			out[k] = vs[0]
		}
	}
	return out
}

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
		// Default redirect policy follows up to 10; that's fine.
	}
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

// isUnsafeIP enumerates the address ranges that should never be reachable
// from a user-supplied URL. Loopback (127/8, ::1), link-local (169.254/16
// — AWS metadata!), RFC 1918 (10/8, 172.16/12, 192.168/16), RFC 4193
// (fc00::/7), multicast, and unspecified all get blocked.
func isUnsafeIP(ip stdnet.IP) bool {
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}

func isSSRFError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "ssrf_blocked")
}

func isTextMIME(mime string) bool {
	// Trim parameters: "text/plain; charset=utf-8" → "text/plain"
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	mime = strings.TrimSpace(mime)
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/json", "application/xml",
		"application/csv", "application/javascript",
		"application/x-yaml", "application/yaml":
		return true
	}
	return false
}
