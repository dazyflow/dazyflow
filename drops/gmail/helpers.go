// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gmail

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/apibase"
	"github.com/dazyflow/dazyflow/drops/internal/google"
	"github.com/dazyflow/dazyflow/drops/internal/params"
)

// maxResponseBytes caps how much of an API response we buffer, so a
// hostile or buggy upstream (reachable via the base_url override) can't
// OOM the daemon by streaming an unbounded body.
const maxResponseBytes = 64 << 20 // 64 MiB

func SetTokenLookup(fn google.TokenLookup) { google.SetTokenLookup(fn) }

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	return google.ResolveToken(ctx, job)
}

var httpBase = apibase.New("https://gmail.googleapis.com/gmail/v1")

func SetHTTPBase(base string) { httpBase.Set(base) }

func baseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "base_url"); b != "" {
		return b
	}
	return httpBase.Get()
}

func gmailDo(ctx context.Context, method, url, token, contentType string, body []byte, timeoutMS int) (int, []byte, error) {
	return google.Do(ctx, method, url, token, contentType, body, timeoutMS, maxResponseBytes)
}

func extractGmailError(body []byte) string { return google.ErrMessage(body, 200) }

// friendlyMessage reduces a flattened message (see flatten) to the friendly
// record shape Search emails emits per match: date / from / subject / body
// plus the ids. Body prefers plain text, then HTML, then the snippet, and is
// capped so one huge email can't bloat a whole result list — Read email
// returns the uncapped body when a single message needs it all.
func friendlyMessage(msg map[string]any) map[string]any {
	headers, _ := msg["headers"].(map[string]any)
	header := func(name string) string {
		for k, v := range headers {
			if strings.EqualFold(k, name) {
				return str(v)
			}
		}
		return ""
	}
	body := str(msg["body_text"])
	if body == "" {
		body = str(msg["body_html"])
	}
	if body == "" {
		body = str(msg["snippet"])
	}
	const maxBody = 20000
	if len(body) > maxBody {
		cut := maxBody
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut-- // don't split a multi-byte character
		}
		body = body[:cut] + "…"
	}
	return map[string]any{
		"id":       str(msg["id"]),
		"threadId": str(msg["threadId"]),
		"date":     header("Date"),
		"from":     header("From"),
		"subject":  header("Subject"),
		"body":     body,
	}
}

func flatten(raw map[string]any) map[string]any {
	out := map[string]any{
		"id":               str(raw["id"]),
		"threadId":         str(raw["threadId"]),
		"snippet":          str(raw["snippet"]),
		"internal_date_ms": str(raw["internalDate"]),
		"raw":              raw,
	}
	if labels, ok := raw["labelIds"].([]any); ok {
		out["labels"] = labels
	}
	if payload, ok := raw["payload"].(map[string]any); ok {
		out["headers"] = extractHeaders(payload)
		if text := findTextPart(payload, "text/plain"); text != "" {
			out["body_text"] = text
		}
		if html := findTextPart(payload, "text/html"); html != "" {
			out["body_html"] = html
		}
	}
	return out
}

func extractHeaders(payload map[string]any) map[string]any {
	out := map[string]any{}
	headers, _ := payload["headers"].([]any)
	for _, h := range headers {
		m, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if name := str(m["name"]); name != "" {
			out[name] = str(m["value"])
		}
	}
	return out
}

func findTextPart(payload map[string]any, mimeType string) string {
	if str(payload["mimeType"]) == mimeType {
		if body, ok := payload["body"].(map[string]any); ok {
			if data := str(body["data"]); data != "" {
				if dec, err := base64.RawURLEncoding.DecodeString(stripB64Pad(data)); err == nil {
					return string(dec)
				}
			}
		}
	}
	parts, _ := payload["parts"].([]any)
	for _, p := range parts {
		if m, ok := p.(map[string]any); ok {
			if found := findTextPart(m, mimeType); found != "" {
				return found
			}
		}
	}
	return ""
}

func stripB64Pad(s string) string {
	for len(s) > 0 && s[len(s)-1] == '=' {
		s = s[:len(s)-1]
	}
	return s
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}
