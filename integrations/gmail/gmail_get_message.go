package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "gmail_get_message",
			Version:        "1.0",
			Label:          "Gmail get message",
			Color:          "#D14836",
			Icon:           "file-input",
			BrandLogo:      "/brands/gmail.svg",
			Category:       "network",
			Provider:       "internal",
			Integration:    "Gmail",
			Tags:           []string{"gmail", "email", "fetch", "google"},
			Description:    "Fetch a single Gmail message by ID. Outputs commonly-needed headers (from/to/subject/date) plus snippet, plus the decoded plain-text body when present. Pair with gmail_search_messages + for_each to walk a search result set.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "message", Label: "Message details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":    {"type":"string","default":"default"},
					"token":      {"type":"string","description":"Raw access token; overrides 'account'."},
					"id":         {"type":"string","description":"Gmail message ID (from gmail_search_messages)."},
					"format":     {"type":"string","enum":["full","metadata","minimal"],"default":"full","description":"How much of the message to fetch. 'full' includes body parts; 'metadata' is headers only (faster); 'minimal' is IDs + labels."},
					"timeout_ms": {"type":"integer","default":15000,"minimum":1}
				},
				"required":["id"]
			}`),
			Idempotent: true,
		},
		Execute: executeGmailGetMessage,
	})
}

// executeGmailGetMessage calls users.messages.get and flattens the
// Gmail-shaped response into a downstream-friendly object:
//
//	{
//	  id, threadId, snippet, labels: [...],
//	  headers: {From, To, Subject, Date, ...},
//	  body_text: "...",          // when a text/plain part exists
//	  internal_date_ms: 17...
//	}
//
// The raw Gmail response is also passed through as `raw` for
// drops that need every detail (attachments, alternative parts).
// Most graphs only need the flattened convenience fields.
func executeGmailGetMessage(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	id, err := params.String(job.Params, "id")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	q := url.Values{}
	q.Set("format", params.StringDefault(job.Params, "format", "full"))

	endpoint := currentHTTPBase() + "/users/me/messages/" + url.PathEscape(id) + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return params.Err(job, "internal", err.Error()), nil
	}
	req.Header.Set("Authorization", "Bearer "+token)

	timeoutMs := params.IntDefault(job.Params, "timeout_ms", 15000)
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return params.Err(job, "send_failed", err.Error()), nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 5 MiB cap; large attachments need the raw API anyway

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return params.Err(job, "gmail_error",
			fmt.Sprintf("Gmail returned %d: %s", resp.StatusCode, extractGmailError(body))), nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return params.Err(job, "parse", err.Error()), nil
	}

	flat := flattenGmailMessage(raw)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"message": {MIME: "application/json", Inline: flat},
		},
	}, nil
}

// flattenGmailMessage turns Gmail's nested payload structure into
// a flat-ish object: the common headers as a map, body text when
// extractable, plus the raw response under "raw" for power users.
//
// Gmail messages are MIME trees: `payload` has `headers`, may have
// `body.data` (single-part) OR `parts: [...]` (multipart). We do a
// depth-first search for the first text/plain part as a reasonable
// default; HTML-only or multipart/alternative emails will need a
// custom transform downstream.
func flattenGmailMessage(raw map[string]any) map[string]any {
	out := map[string]any{
		"id":               stringField(raw, "id"),
		"threadId":         stringField(raw, "threadId"),
		"snippet":          stringField(raw, "snippet"),
		"internal_date_ms": stringField(raw, "internalDate"), // Gmail returns ms-since-epoch as a string
		"raw":              raw,
	}
	if labels, ok := raw["labelIds"].([]any); ok {
		out["labels"] = labels
	}
	payload, _ := raw["payload"].(map[string]any)
	if payload == nil {
		return out
	}
	out["headers"] = extractHeaders(payload)
	if text := findTextPart(payload, "text/plain"); text != "" {
		out["body_text"] = text
	}
	if html := findTextPart(payload, "text/html"); html != "" {
		out["body_html"] = html
	}
	return out
}

func extractHeaders(payload map[string]any) map[string]string {
	out := map[string]string{}
	hdrs, _ := payload["headers"].([]any)
	for _, h := range hdrs {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		name, _ := hm["name"].(string)
		value, _ := hm["value"].(string)
		if name != "" {
			out[name] = value
		}
	}
	return out
}

// findTextPart walks the MIME tree looking for the first part with
// the given mimeType. Returns the decoded body text or "" if not
// found. Gmail base64-URL-encodes part bodies; we decode for the
// caller so downstream consumers don't have to.
func findTextPart(payload map[string]any, mimeType string) string {
	mt := stringField(payload, "mimeType")
	if mt == mimeType {
		if body, ok := payload["body"].(map[string]any); ok {
			if data, ok := body["data"].(string); ok && data != "" {
				if decoded, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(data); err == nil {
					return string(decoded)
				}
				// Gmail occasionally returns base64 with padding even
				// though the docs say URL-no-padding. Try padded.
				if decoded, err := base64.URLEncoding.DecodeString(data); err == nil {
					return string(decoded)
				}
			}
		}
	}
	// Recurse into multipart parts.
	parts, _ := payload["parts"].([]any)
	for _, p := range parts {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if found := findTextPart(pm, mimeType); found != "" {
			return found
		}
	}
	return ""
}
