package net

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "webhook_send",
			Version:     "1.0",
			Label:       "Webhook send",
			Summary:     "POST/PUT/PATCH a JSON or text body to an outbound webhook URL.",
			Description: "Send an HTTP request with a body to an external URL — the outbound counterpart to the webhook trigger. The body comes from the 'body' input (a string, or an object that's JSON-encoded) or params.body. Shares the operator egress allowlist and SSRF guard with http_request.",
			Integration: "Webhook",
			Category:    "network",
			Icon:        "webhook",
			Color:       "#7a6cff",
			Provider:    "internal",
			Tags:        []string{"webhook", "http", "send", "post", "outbound"},
			Examples: []core.ParamsExample{
				{Title: "POST JSON to a webhook", Params: json.RawMessage(`{"url":"https://hooks.example.com/abc","body":{"event":"deploy","ok":true}}`)},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "body", Label: "Request body (string sent as-is; object/array JSON-encoded)"},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Delivery metadata", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"url":{"type":"string","description":"Destination URL."},
					"method":{"type":"string","enum":["POST","PUT","PATCH"],"default":"POST"},
					"body":{"type":"string","description":"Body when the 'body' input is not wired."},
					"content_type":{"type":"string","default":"application/json","description":"Content-Type for a string body."},
					"headers":{"type":"object","description":"Extra request headers."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["url"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeWebhookSend,
	})
}

func executeWebhookSend(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	url, _ := params.StringOpt(job.Params, "url")
	if url == "" {
		return params.Err(job, "bad_param", "'url' is required"), nil
	}
	method := strings.ToUpper(params.StringDefault(job.Params, "method", "POST"))
	if method != "POST" && method != "PUT" && method != "PATCH" {
		return params.Err(job, "bad_param", "method "+method+": only POST, PUT, PATCH allowed"), nil
	}

	headers, err := paramHeaders(job.Params, "headers")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if headers == nil {
		headers = map[string]string{}
	}

	var rawBody []byte
	if in, ok := job.Input["body"]; ok && in.Inline != nil {
		rawBody = encodeWebhookBody(in.Inline, headers, params.StringDefault(job.Params, "content_type", "application/json"))
	} else if pb, ok := job.Params["body"]; ok && pb != nil {
		rawBody = encodeWebhookBody(pb, headers, params.StringDefault(job.Params, "content_type", "application/json"))
	}

	if err := egressAllowed(url); err != nil {
		return params.Err(job, "egress_blocked", err.Error()), nil
	}

	timeout := time.Duration(params.IntDefault(job.Params, "timeout_ms", 15000)) * time.Millisecond
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var reader io.Reader
	if rawBody != nil {
		reader = bytes.NewReader(rawBody)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, url, reader)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := buildClient(timeout, PrivateEgressAllowed()).Do(req)
	if err != nil {
		return params.Err(job, "webhook_http_error", err.Error()), nil
	}
	defer resp.Body.Close()
	text, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := string(text)
		if len(detail) > 512 {
			detail = detail[:512]
		}
		return params.Err(job, "webhook_error", "webhook returned "+resp.Status+": "+detail), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"meta": {MIME: "application/json", Inline: map[string]any{
			"url": url, "method": method, "status": resp.StatusCode, "bytes_sent": len(rawBody), "response": string(text),
		}}},
	}, nil
}

// encodeWebhookBody renders a body value to bytes and sets a Content-Type
// header when the caller didn't supply one: a string goes out as-is under
// the given default type; anything else is JSON-encoded as application/json.
func encodeWebhookBody(v any, headers map[string]string, stringCT string) []byte {
	switch t := v.(type) {
	case string:
		if !hasHeader(headers, "Content-Type") {
			headers["Content-Type"] = stringCT
		}
		return []byte(t)
	case []byte:
		if !hasHeader(headers, "Content-Type") {
			headers["Content-Type"] = stringCT
		}
		return t
	default:
		b, _ := json.Marshal(v)
		if !hasHeader(headers, "Content-Type") {
			headers["Content-Type"] = "application/json"
		}
		return b
	}
}

func hasHeader(h map[string]string, name string) bool {
	for k := range h {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}
