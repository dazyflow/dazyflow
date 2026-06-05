package io

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "http_download",
			Version:     "1.0",
			Label:       "HTTP download",
			Color:       "#5599ee",
			Icon:        "download",
			Category:    "io",
			Provider:    "internal",
			Integration: "HTTP",
			Tags:        []string{"http", "download", "file", "sandbox"},
			Description: "Download a URL to a file in the workspace sandbox, streaming the response to disk (handles bodies too large to hold in memory). Honors per-tenant quotas as it writes and aborts cleanly if the budget or max_bytes is exceeded. Use a scratch:// path for intermediate downloads that should be reclaimed when the run ends. Private-network addresses are blocked by default.",
			Summary:     "Stream a URL to a workspace (or scratch://) file, enforcing quota and a max_bytes ceiling and blocking private-network targets by default.",
			Examples: []core.ParamsExample{
				{
					Title:  "Save a public report to the workspace",
					Params: json.RawMessage(`{"url":"https://example.com/data/export.csv","path":"workspace://imports/export.csv","mkdirs":true}`),
				},
				{
					Title:  "Authenticated download to a scratch path",
					Params: json.RawMessage(`{"url":"https://api.example.com/v1/dump","path":"scratch://dumps/today.bin","headers":{"Authorization":"Bearer ${tenant:EXAMPLE_API_TOKEN}"},"timeout_ms":600000,"max_bytes":524288000}`),
					Notes:  "scratch:// files are cleaned up at the end of the run; bump max_bytes for large dumps.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{{
				Port:  "url",
				Label: "URL (optional, overrides params.url)",
				MIME:  []string{"text/plain"},
			}},
			Outputs: []core.Port{
				{Port: "out", Label: "Downloaded file ref"},
				{Port: "meta", Label: "Status + bytes + content-type (JSON)", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"url":{"type":"string","description":"Absolute URL to download."},
						"path":{"type":"string","format":"workspace-path","description":"Destination path in the sandbox. Prefix with scratch:// for an ephemeral, run-scoped file."},
						"method":{"type":"string","default":"GET","enum":["GET","POST"],"description":"HTTP verb."},
						"headers":{"type":"object","additionalProperties":{"type":"string"},"description":"Request headers. Values may include ${tenant:NAME} secret placeholders (e.g. an Authorization bearer token)."},
						"mkdirs":{"type":"boolean","description":"Create parent directories of path if missing."},
						"timeout_ms":{"type":"integer","default":300000,"minimum":1,"description":"Hard deadline for the whole download, in milliseconds."},
						"max_bytes":{"type":"integer","default":104857600,"minimum":0,"description":"Abort if the download exceeds this many bytes. Default 100 MiB; 0 = unlimited (still bounded by quota)."},
						"expect_status":{"type":"array","items":{"type":"integer"},"description":"Accepted status codes. Empty defaults to 2xx."},
						"allow_private_networks":{"type":"boolean","default":false,"description":"Disable the SSRF guard. Only for intentional local targets."}
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
	if strings.TrimSpace(url) == "" {
		return params.Err(job, "bad_param", "url is required (params.url or the 'url' input)"), nil
	}
	dest, err := params.String(job.Params, "path")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if err := hfnet.EgressAllowed(url); err != nil {
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
	// operator opted in (HAZYFLOW_ALLOW_PRIVATE_EGRESS), else ignored.
	allowPrivate := params.BoolDefault(job.Params, "allow_private_networks", false) && hfnet.PrivateEgressAllowed()

	// Resolve the destination (workspace or scratch://) and open its root.
	root, rel, err := openSandboxRoot(job, dest)
	if err != nil {
		return params.Err(job, "no_sandbox", err.Error()), nil
	}
	defer root.Close()

	req, err := http.NewRequestWithContext(ctx, method, url, nil)
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

	if !downloadStatusOK(resp.StatusCode, paramIntSliceLocal(job.Params, "expect_status")) {
		return params.Err(job, "unexpected_status", fmt.Sprintf("got %d", resp.StatusCode)), nil
	}

	if mkdirs, _ := paramBool(job.Params, "mkdirs"); mkdirs {
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

func downloadURL(job core.Job) string {
	if in, ok := job.Input["url"]; ok {
		if s, ok := in.Inline.(string); ok && s != "" {
			return s
		}
	}
	return params.StringDefault(job.Params, "url", "")
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

func paramIntSliceLocal(p map[string]any, key string) []int {
	raw, ok := p[key].([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(raw))
	for _, v := range raw {
		if f, ok := v.(float64); ok {
			out = append(out, int(f))
		}
	}
	return out
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
