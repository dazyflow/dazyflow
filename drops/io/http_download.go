// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package io

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	neturl "net/url"
	"path"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "http_download",
			Version:     "1.0",
			Label:       "Download a file",
			Subtitle:    "Save a file from a URL",
			Color:       "#5599ee",
			Icon:        "download",
			Category:    "io",
			Provider:    "internal",
			Integration: "HTTP",
			Tags:        []string{"http", "download", "file", "sandbox"},
			Description: "Download a file from a web address and save it in the workspace. The download streams to disk, so files too large to hold in memory are fine; workspace storage limits are respected as it writes. Use a scratch:// path for in-between files that should be cleaned up when the run ends. Private-network addresses are blocked by default.",
			Summary:     "Download a file from a web address and save it in the workspace, ready for the next step to use.",
			Examples: []core.ParamsExample{
				{
					Title:  "Save a public report to the workspace",
					Params: json.RawMessage(`{"url":"https://example.com/data/export.csv","path":"workspace://imports/export.csv","mkdirs":true}`),
				},
				{
					Title:  "Authenticated download to a scratch path",
					Params: json.RawMessage(`{"url":"https://api.example.com/v1/dump","path":"scratch://dumps/today.bin","headers":{"Authorization":"Bearer ${secret.EXAMPLE_API_TOKEN}"},"timeout_ms":600000,"max_bytes":524288000}`),
					Notes:  "scratch:// files are cleaned up at the end of the run; bump max_bytes for large dumps.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{
					// Named after its param so the card shows an inline editable
					// box; a wired value overrides the typed one.
					Port:  "url",
					Label: "URL",
					MIME:  []string{"text/plain"},
				},
				// Optional POST body, pipeable from an upstream node (e.g. a
				// JSON step's output). Untyped so it accepts text, bytes, or a
				// structured value (JSON-marshalled); a wired value overrides
				// the 'body' param. Mirrors http_request's request_body port.
				{Port: "request_body", Label: "Body"},
			},
			Outputs: []core.Port{
				// Only the file is a pin; the structured result (status, bytes,
				// content_type) is still EMITTED under "meta" (see the Execute
				// result) so run records keep it for debugging — it's just not
				// a pin (same as gmail send / sheets append).
				{Port: "out", Label: "Downloaded file"},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"url":{"type":"string","title":"URL","description":"The web address to download. The URL input overrides this when connected."},
						"path":{"type":"string","title":"Save to","format":"workspace-path","description":"Where to save the file in the workspace. Prefix with scratch:// for an in-between file that's cleaned up after the run."},
						"method":{"type":"string","title":"Method","default":"GET","enum":["GET","POST"],"description":"GET fetches the file; POST sends the Body along (for export-style endpoints)."},
						"body":{"type":"string","title":"Body","description":"Body to send with a POST request. The Body input overrides this when connected. May include ${secret.NAME} placeholders."},
						"mkdirs":{"type":"boolean","title":"Create missing folders","description":"Create the folders in 'Save to' if they don't exist yet."},
						"headers":{"type":"object","title":"Headers","additionalProperties":{"type":"string"},"x_advanced":true,"description":"Request headers. Values may include ${secret.NAME} secret placeholders (e.g. an Authorization bearer token)."},
						"timeout_ms":{"type":"integer","default":300000,"minimum":1,"description":"Hard deadline for the whole download, in milliseconds."},
						"max_bytes":{"type":"integer","title":"Max download bytes","default":104857600,"minimum":0,"x_advanced":true,"description":"Abort if the download exceeds this many bytes. Default 100 MiB; 0 = unlimited (still bounded by quota)."},
						"expect_status":{"type":"array","title":"Accepted status codes","items":{"type":"integer"},"x_advanced":true,"description":"Status codes treated as success. Empty defaults to 2xx."},
						"allow_private_networks":{"type":"boolean","title":"Allow private networks","default":false,"x_advanced":true,"description":"Disable the private-address guard. Only for intentional local targets."}
					},
					"required":["url","path"]
				}`,
			),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeHTTPDownload,
	})
}

const defaultDownloadMaxBytes = 100 * 1024 * 1024 // 100 MiB

func executeHTTPDownload(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	url := downloadURL(job)
	if url == "" {
		return params.Err(job, "bad_param", "url is required (params.url or the 'url' input)"), nil
	}
	if err := requireHTTPScheme(url); err != nil {
		return params.Err(job, "bad_url", err.Error()), nil
	}
	dest, err := params.String(job.Params, "path")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
		return params.Err(job, "egress_blocked", err.Error()), nil
	}

	headers, err := downloadHeaders(job.Params)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	method := strings.ToUpper(params.StringDefault(job.Params, "method", "GET"))
	timeoutMs := params.IntDefault(job.Params, "timeout_ms", 300000)
	maxBytes := int64(params.IntDefault(job.Params, "max_bytes", defaultDownloadMaxBytes))
	// allow_private_networks disables the SSRF guard — honored only when the
	// operator opted in (DAZYFLOW_ALLOW_PRIVATE_EGRESS), else ignored.
	allowPrivate := params.BoolDefault(job.Params, "allow_private_networks", false) && hfnet.PrivateEgressAllowed()

	// Resolve the destination (workspace or scratch://) and open its root.
	root, rel, err := openSandboxRoot(job, dest)
	if err != nil {
		return params.Err(job, "no_sandbox", err.Error()), nil
	}
	defer root.Close()

	bodyReader, err := downloadRequestBody(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return params.Err(job, "bad_url", err.Error()), nil
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := hfnet.SafeHTTPClient(time.Duration(timeoutMs)*time.Millisecond, allowPrivate).Do(req)
	if err != nil {
		if hfnet.IsSSRFError(err) {
			return params.Err(job, "ssrf_blocked", err.Error()), nil
		}
		if ctx.Err() != nil {
			return params.Err(job, "cancelled", ctx.Err().Error()), ctx.Err()
		}
		return params.Err(job, "http", err.Error()), nil
	}
	defer resp.Body.Close()

	if !downloadStatusOK(resp.StatusCode, params.IntSlice(job.Params, "expect_status")) {
		return params.Err(job, "unexpected_status", fmt.Sprintf("got %d", resp.StatusCode)), nil
	}

	if mkdirs, _ := params.Bool(job.Params, "mkdirs"); mkdirs {
		if err := root.MkdirAll(path.Dir(rel), 0o755); err != nil {
			if isSandboxEscape(err) {
				return params.Err(job, "sandbox_escape", fmt.Sprintf("mkdirs %q escapes its sandbox root", dest)), nil
			}
			return params.Err(job, "io", fmt.Sprintf("mkdir: %v", err)), nil
		}
	}
	out, err := root.Create(rel)
	if err != nil {
		if isSandboxEscape(err) {
			return params.Err(job, "sandbox_escape", fmt.Sprintf("dest %q escapes its sandbox root", dest)), nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			// The parent folder doesn't exist and mkdirs is off — point the
			// user at the fix instead of surfacing a raw ENOENT.
			return params.Err(job, "io", fmt.Sprintf("can't save to %q — its folder doesn't exist yet. Turn on 'Create missing folders', or pick an existing folder.", dest)), nil
		}
		return params.Err(job, "io", fmt.Sprintf("create %q: %v", dest, err)), nil
	}
	defer out.Close()

	written, errResult := streamToFile(job, resp.Body, out, root, rel, maxBytes)
	if errResult != nil {
		return *errResult, nil
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			// Ref preserves the scheme so a downstream node resolves it the same.
			"out": {MIME: contentType, Ref: dest},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"status":       resp.StatusCode,
				"bytes":        written,
				"content_type": contentType,
			}},
		},
	}, nil
}

// streamToFile copies src→dst in bounded chunks, enforcing max_bytes and
// the tenant quota AS IT WRITES (reserve-and-stream): each chunk is
// reserved against the per-tenant budget before it lands, so concurrent
// downloads can't collectively bust the limit, and an over-budget or
// over-cap download aborts mid-stream with the partial file removed.
// Returns the bytes written, or a non-nil *Result describing the failure.
func streamToFile(job core.Job, src io.Reader, dst io.Writer, root sandboxRoot, rel string, maxBytes int64) (int64, *core.Result) {
	buf := make([]byte, 64*1024)
	var written int64
	var releases []func()
	committed := false
	defer func() {
		for _, r := range releases {
			r() // drop in-flight reservations (commit: bytes now on disk; abort: file removed below)
		}
		if !committed {
			_ = root.Remove(rel) // no half-written file left behind
		}
	}()

	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if maxBytes > 0 && written+int64(n) > maxBytes {
				r := params.Err(job, "too_large", fmt.Sprintf("download exceeds max_bytes=%d", maxBytes))
				return 0, &r
			}
			if job.QuotaLimit > 0 {
				// Snapshot check (the only enforcement without a live
				// reserver, e.g. unit tests) ...
				if job.QuotaUsed+written+int64(n) > job.QuotaLimit {
					r := params.Err(job, "quota_exceeded", fmt.Sprintf("download would push tenant past %d", job.QuotaLimit))
					return 0, &r
				}
				// ... plus an atomic reservation (closes the concurrent race).
				rel, qErr := reserveQuota(job.Tenant, int64(n))
				if qErr != nil {
					if errors.Is(qErr, core.ErrQuotaExceeded) {
						r := params.Err(job, "quota_exceeded", "download would exceed tenant quota")
						return 0, &r
					}
					r := params.Err(job, "quota", fmt.Sprintf("reserve quota: %v", qErr))
					return 0, &r
				}
				releases = append(releases, rel)
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				r := params.Err(job, "io", fmt.Sprintf("write: %v", werr))
				return 0, &r
			}
			written += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			r := params.Err(job, "io", fmt.Sprintf("read response: %v", rerr))
			return 0, &r
		}
	}
	committed = true
	return written, nil
}

// sandboxRoot is the subset of *os.Root streamToFile needs (Remove for
// partial-file cleanup), kept small so the function is easy to test.
type sandboxRoot interface {
	Remove(name string) error
}

// downloadURL resolves the URL in priority order: the 'url' input port (a
// string or raw bytes from an upstream node) over params.url. Whitespace is
// trimmed so a trailing newline from an upstream text step doesn't break the
// request. A non-text or empty input falls through to the param.
func downloadURL(job core.Job) string {
	if in, ok := job.Input["url"]; ok {
		switch v := in.Inline.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case []byte:
			if s := strings.TrimSpace(string(v)); s != "" {
				return s
			}
		}
	}
	return strings.TrimSpace(params.StringDefault(job.Params, "url", ""))
}

// downloadRequestBody builds the optional request body for POST: the
// 'request_body' input port wins (so a body can be piped from an upstream
// node — a string, raw bytes, or a structured value JSON-marshalled), else
// params.body. Returns nil when neither is set (a bodyless GET/POST). Mirrors
// http_request's buildRequestBody so the two HTTP drops behave identically.
func downloadRequestBody(job core.Job) (io.Reader, error) {
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

// requireHTTPScheme rejects anything that isn't an http:// or https://
// URL (file://, ftp://, scheme-less, unparseable) with a clear message
// rather than letting the transport fail later with an opaque
// "unsupported protocol scheme". Shared by http_download and
// http_upload so both gate the scheme the same way.
func requireHTTPScheme(rawURL string) error {
	u, err := neturl.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("url must be an http:// or https:// address")
	}
	return nil
}

func downloadHeaders(p map[string]any) (map[string]string, error) {
	raw, ok := p["headers"]
	if !ok || raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("headers must be an object")
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("header %q must be a string", k)
		}
		out[k] = s
	}
	return out, nil
}

func downloadStatusOK(got int, expect []int) bool {
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
