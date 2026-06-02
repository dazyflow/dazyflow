package io

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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
			ID:          "http_upload",
			Version:     "1.0",
			Label:       "HTTP upload",
			Color:       "#5599ee",
			Icon:        "upload",
			Category:    "io",
			Provider:    "internal",
			Integration: "HTTP",
			Tags:        []string{"http", "upload", "file", "sandbox"},
			Description: "Upload a file from the workspace sandbox to a URL, streaming it from disk. Raw mode (default) PUTs the file bytes directly — what S3/GCS/Azure presigned URLs expect; multipart mode POSTs it as a form field for APIs that want multipart/form-data. Reads scratch:// paths too. Private-network addresses are blocked by default.",
			Summary:     "Stream a sandbox file to a remote URL as a raw PUT body (presigned-URL style) or a multipart/form-data POST.",
			Examples: []core.ParamsExample{
				{
					Title:  "PUT to an S3 presigned URL",
					Params: json.RawMessage(`{"url":"https://bucket.s3.amazonaws.com/uploads/report.xlsx?X-Amz-Signature=...","path":"workspace://reports/report.xlsx","content_type":"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}`),
				},
				{
					Title:  "Multipart POST to a form-data API",
					Params: json.RawMessage(`{"url":"https://api.example.com/v1/attachments","path":"workspace://uploads/photo.jpg","multipart":true,"field_name":"file","filename":"photo.jpg","headers":{"Authorization":"Bearer ${secret:EXAMPLE_API_TOKEN}"}}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{{
				Port:  "in",
				Label: "File ref to upload (optional, overrides params.path)",
			}},
			Outputs: []core.Port{
				{Port: "response_body", Label: "Response body"},
				{Port: "meta", Label: "Status + bytes sent (JSON)"},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"url":{"type":"string","description":"Absolute destination URL."},
						"path":{"type":"string","format":"workspace-path","description":"Sandbox file to upload (or wire the 'in' input). scratch:// supported."},
						"multipart":{"type":"boolean","default":false,"description":"false = raw body (PUT the bytes, e.g. presigned URLs); true = multipart/form-data POST."},
						"method":{"type":"string","enum":["PUT","POST"],"description":"Defaults to PUT for raw, POST for multipart."},
						"field_name":{"type":"string","default":"file","description":"Multipart form field name (multipart mode only)."},
						"filename":{"type":"string","description":"Filename sent in multipart mode. Defaults to the base name of path."},
						"content_type":{"type":"string","description":"Content-Type for raw mode. Defaults to a guess from the extension."},
						"headers":{"type":"object","additionalProperties":{"type":"string"},"description":"Request headers (e.g. Authorization). Values may include ${tenant:NAME} secrets."},
						"timeout_ms":{"type":"integer","default":300000,"minimum":1,"description":"Hard deadline for the whole upload, in milliseconds."},
						"expect_status":{"type":"array","items":{"type":"integer"},"description":"Accepted status codes. Empty defaults to 2xx."},
						"allow_private_networks":{"type":"boolean","default":false,"description":"Disable the SSRF guard. Only for intentional local targets."}
					},
					"required":["url"]
				}`,
			),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeHTTPUpload,
	})
}

func executeHTTPUpload(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	url, err := params.String(job.Params, "url")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if err := hfnet.EgressAllowed(url); err != nil {
		return params.Err(job, "egress_blocked", err.Error()), nil
	}
	srcPath := uploadSrcPath(job)
	if strings.TrimSpace(srcPath) == "" {
		return params.Err(job, "bad_param", "path is required (params.path or the 'in' input)"), nil
	}
	headers, err := downloadHeaders(job.Params) // shared param-headers parser
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	multi := params.BoolDefault(job.Params, "multipart", false)
	timeoutMs := params.IntDefault(job.Params, "timeout_ms", 300000)
	// allow_private_networks disables the SSRF guard — honored only when the
	// operator opted in (HAZYFLOW_ALLOW_PRIVATE_EGRESS), else ignored.
	allowPrivate := params.BoolDefault(job.Params, "allow_private_networks", false) && hfnet.PrivateEgressAllowed()

	// Open the source file from the sandbox (workspace or scratch://).
	root, rel, err := openSandboxRoot(job, srcPath)
	if err != nil {
		return params.Err(job, "no_sandbox", err.Error()), nil
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		if isSandboxEscape(err) {
			return params.Err(job, "sandbox_escape", fmt.Sprintf("path %q escapes its sandbox root", srcPath)), nil
		}
		return params.Err(job, "io", fmt.Sprintf("open %q: %v", srcPath, err)), nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return params.Err(job, "io", fmt.Sprintf("stat %q: %v", srcPath, err)), nil
	}

	var (
		body          io.Reader
		contentType   string
		contentLength int64 = -1
		method        string
	)
	if multi {
		method = strings.ToUpper(params.StringDefault(job.Params, "method", "POST"))
		field := params.StringDefault(job.Params, "field_name", "file")
		filename := params.StringDefault(job.Params, "filename", path.Base(rel))
		// Stream the multipart body through a pipe so a large file never
		// sits in memory; the writer goroutine copies the file then closes.
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		contentType = mw.FormDataContentType()
		go func() {
			part, perr := mw.CreateFormFile(field, filename)
			if perr == nil {
				_, perr = io.Copy(part, f)
			}
			if perr == nil {
				perr = mw.Close()
			}
			_ = pw.CloseWithError(perr) // propagates a read error to the request
		}()
		body = pr
	} else {
		method = strings.ToUpper(params.StringDefault(job.Params, "method", "PUT"))
		contentType = params.StringDefault(job.Params, "content_type", guessMIMEByExt(rel))
		contentLength = info.Size() // known for raw — lets the server size the upload
		body = f
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return params.Err(job, "bad_url", err.Error()), nil
	}
	req.Header.Set("Content-Type", contentType)
	if contentLength >= 0 {
		req.ContentLength = contentLength
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

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // upload responses are small
	respCT := resp.Header.Get("Content-Type")
	var inline any = respBody
	if isTextMIME(respCT) {
		inline = string(respBody)
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"response_body": {MIME: respCT, Inline: inline},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"status":     resp.StatusCode,
				"bytes_sent": info.Size(),
			}},
		},
	}, nil
}

// uploadSrcPath takes the file path from the 'in' input ref (so an
// upstream file_write/http_download can feed it) or params.path.
func uploadSrcPath(job core.Job) string {
	if in, ok := job.Input["in"]; ok && in.Ref != "" {
		return in.Ref
	}
	return params.StringDefault(job.Params, "path", "")
}
