// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DazydClient signs requests and parses responses over the dzd /api/v1 gateway;
// the MCP tools wrapping it do all the shape massaging. Auth is a bearer token
// from $DAZYFLOW_API_KEY, never logged.
type DazydClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func NewDazydClient(baseURL, token string) *DazydClient {
	return &DazydClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

type Whoami struct {
	Subject   string   `json:"subject"`
	Tenant    string   `json:"tenant"`
	Workspace string   `json:"workspace"`
	Roles     []any    `json:"roles"`
	Perms     []string `json:"permissions"`
}

func (c *DazydClient) Whoami(ctx context.Context) (Whoami, error) {
	var w Whoami
	if err := c.do(ctx, http.MethodGet, "/me", nil, &w); err != nil {
		return Whoami{}, err
	}
	return w, nil
}

func (c *DazydClient) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/api/v1"+path, rdr)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key := idempotencyKeyFromContext(ctx); key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("dazyflow %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code, message, doc, details := parseErrorEnvelope(respBody)
		if message == "" {
			message = strings.TrimSpace(string(respBody))
		}
		return &HTTPError{
			Status:  resp.StatusCode,
			Path:    method + " " + path,
			Body:    strings.TrimSpace(string(respBody)),
			Code:    code,
			Message: message,
			Details: details,
			Doc:     doc,
		}
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

// HTTPError lets tools surface the daemon's message verbatim through
// ToolCallResult.IsError rather than as a JSON-RPC error.
//
// A structured envelope from a spec-aligned route fills Code, Message, Details
// and Doc, keeping the raw Body too; the legacy `{"error":"..."}` shape fills
// only Message. Tools branch on Code where present — machine-readable beats
// parsing English.
type HTTPError struct {
	Status  int
	Path    string
	Body    string // raw response body, useful for diagnostics
	Code    string
	Message string
	Details []ErrorDetail
	Doc     string
}

// ErrorDetail mirrors the gateway's per-field validation shape, restated rather
// than imported so mcp stays independent of daemon.
type ErrorDetail struct {
	Field string `json:"field,omitempty"`
	Issue string `json:"issue,omitempty"`
}

func (e *HTTPError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s: %d %s (code: %s)", e.Path, e.Status, e.Message, e.Code)
	}
	return fmt.Sprintf("%s: %d %s", e.Path, e.Status, e.Body)
}

func (e *HTTPError) ToToolPayload() map[string]any {
	payload := map[string]any{
		"status":  e.Status,
		"path":    e.Path,
		"message": e.Message,
	}
	if e.Code != "" {
		payload["code"] = e.Code
	}
	if len(e.Details) > 0 {
		payload["details"] = e.Details
	}
	if e.Doc != "" {
		payload["doc"] = e.Doc
	}
	return payload
}

func parseErrorEnvelope(body []byte) (code, message, doc string, details []ErrorDetail) {
	var legacy struct {
		Error any `json:"error"`
	}
	if err := json.Unmarshal(body, &legacy); err != nil {
		return
	}
	switch e := legacy.Error.(type) {
	case string:
		message = e
		return
	case map[string]any:
		if s, ok := e["code"].(string); ok {
			code = s
		}
		if s, ok := e["message"].(string); ok {
			message = s
		}
		if s, ok := e["doc"].(string); ok {
			doc = s
		}
		if raw, ok := e["details"].([]any); ok {
			for _, d := range raw {
				if m, ok := d.(map[string]any); ok {
					var ed ErrorDetail
					if f, ok := m["field"].(string); ok {
						ed.Field = f
					}
					if i, ok := m["issue"].(string); ok {
						ed.Issue = i
					}
					details = append(details, ed)
				}
			}
		}
	}
	return
}

func pathSegment(s string) string {
	return url.PathEscape(s)
}

// buildQuery skips empty values and returns "" when none survive, so callers
// append it unconditionally. Encoding and the sorted key order are not observable
// to a conforming server.
func buildQuery(params map[string]string) string {
	v := url.Values{}
	for k, val := range params {
		if val == "" {
			continue
		}
		v.Set(k, val)
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

func composeFlowID(tenant, workspace, id string) string {
	return url.PathEscape(tenant + "/" + workspace + "/" + id)
}

func (c *DazydClient) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *DazydClient) Post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c *DazydClient) Put(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPut, path, body, out)
}

// Patch sends an RFC 7396 JSON Merge Patch under a generic application/json
// Content-Type, which the daemon's handler accepts; a server needing the
// merge-patch MIME type to differentiate cannot rely on it.
func (c *DazydClient) Patch(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPatch, path, body, out)
}

func (c *DazydClient) Delete(ctx context.Context, path string) error {
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// idempotencyKeyCtx threads the key through the context rather than the method
// signatures, which stay unchanged.
type idempotencyKeyCtx struct{}

func withIdempotencyKey(ctx context.Context, key string) context.Context {
	if key == "" {
		return ctx
	}
	return context.WithValue(ctx, idempotencyKeyCtx{}, key)
}

func idempotencyKeyFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(idempotencyKeyCtx{}).(string); ok {
		return v
	}
	return ""
}
