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
	"git.sr.ht/~klahr/hazyflow/drops/internal/mimetype"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "http_upload",
			Version:     "1.0",
			Label:       "HTTP",
			Subtitle:    "Upload file",
			Color:       "#5599ee",
			Icon:        "upload",
			Category:    "io",
			Provider:    "internal",
			Integration: "HTTP",
			Tags:        []string{"http", "upload", "file", "sandbox"},
			Description: "Send a workspace file to a web address, streaming it from disk. By default the file bytes are sent directly — what upload links from S3/GCS/Azure expect; turn on 'Send as form upload' for services that want the file as a form attachment. Reads scratch:// paths too. Private-network addresses are blocked by default.",
			Summary:     "Send a workspace file to a web address — directly (upload links) or as a form attachment.",
			Examples: []core.ParamsExample{
				{
					Title:  "PUT to an S3 presigned URL",
					Params: json.RawMessage(`{"url":"https://bucket.s3.amazonaws.com/uploads/report.xlsx?X-Amz-Signature=...","path":"workspace://reports/report.xlsx","content_type":"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}`),
				},
				{
					Title:  "Multipart POST to a form-data API",
					Params: json.RawMessage(`{"url":"https://api.example.com/v1/attachments","path":"workspace://uploads/photo.jpg","multipart":true,"field_name":"file","filename":"photo.jpg","headers":{"Authorization":"Bearer ${secret.EXAMPLE_API_TOKEN}"}}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{{
				// A wired file (e.g. from file_write / http_download) overrides
				// the 'path' param.
				Port:  "in",
				Label: "File",
			}},
			Outputs: []core.Port{
				// Only the response is a pin; the structured result (status,
				// bytes sent) is still EMITTED under "meta" (see the Execute
				// result) so run records keep it for debugging — it's just not
				// a pin (same as gmail send / sheets append).
				{Port: "response_body", Label: "Response"},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"url":{"type":"string","title":"URL","description":"The web address to upload to."},
						"path":{"type":"string","title":"File to upload","format":"workspace-path","description":"The workspace file to send (or wire the File input). scratch:// supported."},
						"multipart":{"type":"boolean","title":"Send as form upload","default":false,"description":"Off = send the file bytes directly (what upload links expect). On = send as a form attachment for services that ask for one."},
						"method":{"type":"string","title":"Method","enum":["PUT","POST"],"description":"Defaults to PUT for a direct upload, POST for a form upload."},
						"field_name":{"type":"string","title":"Form field name","default":"file","x_advanced":true,"description":"Field name used for a form upload."},
						"filename":{"type":"string","title":"Filename","x_advanced":true,"description":"Filename sent with a form upload. Defaults to the file's own name."},
						"content_type":{"type":"string","title":"Content type","x_advanced":true,"description":"Content-Type for a direct upload. Defaults to a guess from the file extension."},
						"headers":{"type":"object","title":"Headers","additionalProperties":{"type":"string"},"x_advanced":true,"description":"Request headers (e.g. Authorization). Values may include ${secret.NAME} secrets."},
						"timeout_ms":{"type":"integer","default":300000,"minimum":1,"description":"Hard deadline for the whole upload, in milliseconds."},
						"expect_status":{"type":"array","title":"Accepted status codes","items":{"type":"integer"},"x_advanced":true,"description":"Status codes treated as success. Empty defaults to 2xx."},
						"allow_private_networks":{"type":"boolean","title":"Allow private networks","default":false,"x_advanced":true,"description":"Disable the private-address guard. Only for intentional local targets."}
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
	if err := requireHTTPScheme(url); err != nil {
		return params.Err(job, "bad_url", err.Error()), nil
	}
	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
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
		// Guarantee the writer goroutine can always exit. It blocks on
		// pw.Write / mw.Close until something reads pr — normally the HTTP
		// transport. But if we return before Do reads it (NewRequestWithContext
		// error below) or the request is cancelled mid-write, nothing drains
		// pr and the goroutine would leak. Closing the read end on return makes
		// any pending write return ErrClosedPipe, unblocking it.
		defer pr.Close()
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
		contentType = params.StringDefault(job.Params, "content_type", mimetype.GuessByExt(rel))
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

	if !downloadStatusOK(resp.StatusCode, params.IntSlice(job.Params, "expect_status")) {
		return params.Err(job, "unexpected_status", fmt.Sprintf("got %d", resp.StatusCode)), nil
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // upload responses are small
	respCT := resp.Header.Get("Content-Type")
	var inline any = respBody
	if mimetype.IsText(respCT) {
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
