// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gmail hosts the native Gmail connectors (gmail_search_messages,
// gmail_get_message, gmail_send_email), migrated from the scripted TS
// drops. They authenticate with Google OAuth (the "google" provider),
// resolved via the SetTokenLookup hook the daemon wires at startup, or an
// explicit `token` param.
package gmail

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/apibase"
	"git.sr.ht/~klahr/dazyflow/drops/internal/google"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
)

// maxResponseBytes caps how much of an API response we buffer, so a
// hostile or buggy upstream (reachable via the base_url override) can't
// OOM the daemon by streaming an unbounded body.
const maxResponseBytes = 64 << 20 // 64 MiB

// SetTokenLookup wires the shared Google OAuth token resolver (one provider
// serves every Google connector — see drops/internal/google). Retained as a
// package entry point for tests.
func SetTokenLookup(fn google.TokenLookup) { google.SetTokenLookup(fn) }

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	return google.ResolveToken(ctx, job)
}

// --- cursor (watermark) store -----------------------------------------------

// CursorReader returns the stored value for an exact tenant/name, or
// ("", nil) when nothing has been stored yet (first fire). CursorWriter
// persists one. The daemon wires these to the encrypted secret store under a
// reserved "cursor." prefix (hidden from the Credentials UI) via
// SetCursorStore — mirrors gform.SetCursorStore. gmail_search_messages uses
// it for its opt-in "only new since last run" watermark.
type (
	CursorReader func(ctx context.Context, tenant, name string) (string, error)
	CursorWriter func(ctx context.Context, tenant, name, value string) error
)

var (
	cursorMu     sync.RWMutex
	cursorReader CursorReader
	cursorWriter CursorWriter
)

func SetCursorStore(r CursorReader, w CursorWriter) {
	cursorMu.Lock()
	defer cursorMu.Unlock()
	cursorReader, cursorWriter = r, w
}

func readCursor(ctx context.Context, tenant, name string) string {
	cursorMu.RLock()
	r := cursorReader
	cursorMu.RUnlock()
	if r == nil {
		return ""
	}
	v, err := r(ctx, tenant, name)
	if err != nil {
		return "" // treat any read failure as "start from the beginning"
	}
	return v
}

func writeCursor(ctx context.Context, tenant, name, value string) error {
	cursorMu.RLock()
	w := cursorWriter
	cursorMu.RUnlock()
	if w == nil {
		return nil
	}
	return w(ctx, tenant, name, value)
}

var httpBase = apibase.New("https://gmail.googleapis.com/gmail/v1")

// SetHTTPBase swaps the Gmail API root (tests point it at httptest).
func SetHTTPBase(base string) { httpBase.Set(base) }

// baseURL honors a per-job base_url verbatim (no trailing-slash trim — Gmail
// endpoints are concatenated as-is by the callers), else the package default.
func baseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "base_url"); b != "" {
		return b
	}
	return httpBase.Get()
}

// gmailDo runs one authenticated Gmail API call. Returns status + body.
func gmailDo(ctx context.Context, method, url, token, contentType string, body []byte, timeoutMS int) (int, []byte, error) {
	return google.Do(ctx, method, url, token, contentType, body, timeoutMS, maxResponseBytes)
}

// extractGmailError pulls error.message out of a Gmail error body.
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

// flatten turns a raw Gmail message into convenience fields: id, threadId,
// snippet, the headers map, and decoded text/html bodies. The raw payload
// is preserved under "raw" for advanced use. Mirrors the scripted drop.
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

// stripB64Pad removes any '=' padding so RawURLEncoding (which rejects it)
// can decode Gmail's base64url payloads whether or not they're padded.
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
