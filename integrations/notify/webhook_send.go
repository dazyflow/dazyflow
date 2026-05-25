package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "webhook_send",
			Version:        "1.0",
			Label:          "Webhook send",
			Color:          "#7a6cff",
			Icon:           "webhook",
			Category:       "network",
			Provider:       "internal",
			Integration:    "Webhook",
			Tags:           []string{"webhook", "http", "post", "notify", "slack", "discord", "teams"},
			Description:    "POST (or PUT/PATCH) a payload to any webhook URL — Slack, Discord, Teams, PagerDuty, ntfy, or any custom endpoint. Body comes from the input port if connected (objects auto-marshal to JSON; strings/bytes sent as-is), otherwise from params.body. URL supports ${env:NAME} / ${secret:NAME} substitution so credentials don't land in graph JSON.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "body", Label: "Request body (overrides params.body)"},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Delivery metadata (JSON)", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"url":          {"type":"string","description":"Full webhook URL. Use ${env:NAME} or ${secret:NAME} to avoid embedding credentials in the graph."},
					"method":       {"type":"string","enum":["POST","PUT","PATCH"],"default":"POST","description":"HTTP method. POST is the right answer 99% of the time."},
					"body":         {"description":"Default body when no input port is wired. Strings/bytes are sent as-is; anything else is JSON-marshaled and the Content-Type is forced to application/json."},
					"content_type": {"type":"string","default":"application/json","description":"Content-Type header. Auto-overridden to application/json when the body is a non-string value."},
					"headers":      {"type":"object","additionalProperties":{"type":"string"},"description":"Extra request headers (e.g. {\"Authorization\":\"Bearer ${env:TOKEN}\"})."},
					"timeout_ms":   {"type":"integer","default":15000,"minimum":1,"description":"HTTP timeout in milliseconds."}
				},
				"required":["url"]
			}`),
			Idempotent:  false, // Receivers usually deduplicate via idempotency keys; we can't assume.
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeWebhookSend,
	})
}

// executeWebhookSend POSTs a payload to a webhook URL. The body
// resolution order matches the convention used by ntfy: input port
// wins, then params.body, then empty. Empty body is allowed because
// some webhook endpoints treat the POST itself as the trigger
// (PagerDuty's "Acknowledge" links work this way).
//
// Body encoding:
//   - string / []byte → sent verbatim, params.content_type honored
//     (default application/json so the common "Slack text" case
//     works without ceremony)
//   - anything else → JSON-marshaled, Content-Type forced to
//     application/json regardless of params.content_type, since
//     shipping a map with content_type=text/plain would always be a
//     bug
//
// Non-2xx responses surface as `webhook_error` with the status code
// and a truncated response excerpt. Receivers like Slack often put
// the "why this failed" detail (`invalid_payload`, `channel_not_found`)
// in the response body, so swallowing it would lose the only useful
// diagnostic.
func executeWebhookSend(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	url, err := paramString(job.Params, "url")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	method := strings.ToUpper(paramStringDefault(job.Params, "method", "POST"))
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return errResult(job, "bad_param",
			fmt.Sprintf("method %q: only POST, PUT, PATCH allowed", method)), nil
	}

	contentType := paramStringDefault(job.Params, "content_type", "application/json")
	var body []byte
	if input, ok := job.Input["body"]; ok && input.Inline != nil {
		body, contentType, err = encodeWebhookBody(input.Inline, contentType)
		if err != nil {
			return errResult(job, "bad_input", err.Error()), nil
		}
	} else if v, ok := job.Params["body"]; ok && v != nil {
		body, contentType, err = encodeWebhookBody(v, contentType)
		if err != nil {
			return errResult(job, "bad_param", err.Error()), nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return errResult(job, "bad_url", err.Error()), nil
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range webhookHeaders(job.Params) {
		req.Header.Set(k, v)
	}

	timeoutMs := paramIntDefault(job.Params, "timeout_ms", 15000)
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}

	emitProgress(progress, job, 0.3, method+" "+url)
	resp, err := client.Do(req)
	if err != nil {
		return errResult(job, "send_failed", err.Error()), nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errResult(job, "webhook_error",
			fmt.Sprintf("webhook returned %d: %s",
				resp.StatusCode, strings.TrimSpace(string(respBody)))), nil
	}
	emitProgress(progress, job, 1.0, fmt.Sprintf("delivered (%d)", resp.StatusCode))

	meta := map[string]any{
		"url":        url,
		"method":     method,
		"status":     resp.StatusCode,
		"bytes_sent": len(body),
		"response":   string(respBody),
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
}

// encodeWebhookBody turns an arbitrary inline value into bytes + the
// appropriate Content-Type. The caller's content_type wins for raw
// types (string/bytes); for everything else we force
// application/json since serializing a Go map with text/plain set
// would be a foot-gun.
func encodeWebhookBody(v any, fallbackCT string) ([]byte, string, error) {
	switch x := v.(type) {
	case string:
		return []byte(x), fallbackCT, nil
	case []byte:
		return x, fallbackCT, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, "", fmt.Errorf("marshal body: %w", err)
	}
	return b, "application/json", nil
}

// webhookHeaders reads params.headers as a map[string]string. Skips
// non-string values rather than erroring — the schema constrains the
// shape, and quietly dropping a bad value is friendlier than failing
// the whole delivery over a typo in one header.
func webhookHeaders(params map[string]any) map[string]string {
	v, ok := params["headers"]
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}
