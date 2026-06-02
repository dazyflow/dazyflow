// Package gmail hosts the native Gmail connectors (gmail_search_messages,
// gmail_get_message, gmail_send_email), migrated from the scripted TS
// drops. They authenticate with Google OAuth (the "google" provider),
// resolved via the SetTokenLookup hook the daemon wires at startup, or an
// explicit `token` param.
package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/drops/internal/sandbox"
)

type TokenLookup func(ctx context.Context, account string) (string, error)

var (
	tokenLookupMu sync.RWMutex
	tokenLookup   TokenLookup
)

func SetTokenLookup(fn TokenLookup) {
	tokenLookupMu.Lock()
	defer tokenLookupMu.Unlock()
	tokenLookup = fn
}

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	if t, _ := params.StringOpt(job.Params, "token"); t != "" {
		return t, nil
	}
	account, _ := params.StringOpt(job.Params, "account")
	if account == "" {
		account = "default"
	}
	tokenLookupMu.RLock()
	fn := tokenLookup
	tokenLookupMu.RUnlock()
	if fn == nil {
		return "", fmt.Errorf("no Gmail token: pass `token` directly or connect a Google account via /api/v1/oauth/google/authorize")
	}
	tok, err := fn(ctx, account)
	if err != nil {
		return "", fmt.Errorf("lookup token for account %q: %w", account, err)
	}
	if tok == "" {
		return "", fmt.Errorf("google account %q is not connected", account)
	}
	return tok, nil
}

var (
	httpBaseMu sync.RWMutex
	httpBase   = "https://gmail.googleapis.com/gmail/v1"
)

// SetHTTPBase swaps the Gmail API root (tests point it at httptest).
func SetHTTPBase(base string) {
	httpBaseMu.Lock()
	defer httpBaseMu.Unlock()
	httpBase = base
}

func baseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "base_url"); b != "" {
		return b
	}
	httpBaseMu.RLock()
	defer httpBaseMu.RUnlock()
	return httpBase
}

// gmailDo runs one authenticated Gmail API call. Returns status + body.
func gmailDo(ctx context.Context, method, url, token, contentType string, body []byte, timeoutMS int) (int, []byte, error) {
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

// extractGmailError pulls error.message out of a Gmail error body.
func extractGmailError(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	if len(body) > 200 {
		return string(body[:200])
	}
	return string(body)
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

// readRefBytes returns the bytes behind an input Ref: inline []byte/string,
// or a sandbox file when the ref carries a path.
func readRefBytes(job core.Job, ref core.Ref) ([]byte, error) {
	switch v := ref.Inline.(type) {
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	}
	if ref.Ref == "" {
		return nil, fmt.Errorf("attachment has no inline bytes and no path")
	}
	root, rel, err := sandbox.OpenRoot(job, ref.Ref)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
