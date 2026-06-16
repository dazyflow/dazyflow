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
			Label:       "Webhook",
			Subtitle:    "Send",
			Summary:     "Send data to a webhook URL — tell another service that something happened.",
			Description: "Send data to a webhook URL — the outbound counterpart to the webhook trigger. The URL and Body can be typed on the step or wired in from upstream (the matching input overrides the param); text is sent as-is, an object or list is sent as JSON. Private-network addresses are blocked by default.",
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
				// Named after their params so the card shows inline editable
				// boxes (Unreal-style); a wired value overrides the typed one.
				// url first — it's the primary input.
				{Port: "url", Label: "URL", MIME: []string{"text/plain"}},
				{Port: "body", Label: "Body"},
			},
			// No declared outputs: sending a webhook is a "do" step — chain via
			// the pass-through pin. The delivery details (url, method, status,
			// bytes sent, response text) are still EMITTED under "meta" for run
			// records, just not a pin (same as gmail send / ntfy).
			Outputs: []core.Port{},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"url":{"type":"string","title":"URL","description":"The webhook address to send to. The URL input overrides this when connected."},
					"method":{"type":"string","title":"Method","enum":["POST","PUT","PATCH"],"default":"POST"},
					"body":{"type":"string","title":"Body","description":"Text to send. The Body input overrides this when connected."},
					"content_type":{"type":"string","title":"Content type","default":"application/json","x_advanced":true,"description":"Content-Type sent with a text body."},
					"headers":{"type":"object","title":"Headers","x_advanced":true,"description":"Extra request headers."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
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
	// The URL input overrides the param when wired (resolveURL, shared with
	// http_request) — so the destination can be computed by an upstream node.
	url := resolveURL(job)
	if url == "" {
		return params.Err(job, "bad_param", "'url' is required — set it or wire the URL input"), nil
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

	if err := EgressAllowedFor(ctx, url); err != nil {
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
