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

// DazydClient is a thin HTTP client over the dzd /api/v1 gateway. It
// is intentionally minimal — the MCP tools that wrap it do all the
// shape massaging; the client just signs requests and parses
// responses.
//
// Auth is bearer-token, supplied via $DAZYFLOW_API_KEY in the MCP
// client config. The token is never logged.
type DazydClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewDazydClient builds a client against baseURL with token, using a
// 30s default HTTP timeout. Callers can override .HTTP for tests.
func NewDazydClient(baseURL, token string) *DazydClient {
	return &DazydClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Whoami resolves the bearer's principal — the MCP tools use the
// returned tenant/workspace as defaults so the LLM doesn't have to
// pass them on every call.
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

// do is the single transport choke point: applies the bearer
// header, encodes JSON bodies, surfaces non-2xx as an httpError so
// tools can map them to ToolCallResult.IsError without re-parsing
// the message.
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
		// Fall back to the raw body when the envelope didn't parse —
		// gives the LLM something to work with even on transport-layer
		// or stray non-JSON errors.
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

// HTTPError is what do returns on non-2xx. Tools type-assert against
// it so they can surface the daemon's error message verbatim through
// ToolCallResult.IsError rather than as a JSON-RPC error.
//
// When the response body is the structured ErrorEnvelope shape
// (`{"error":{"code":"...","message":"..."}}`) emitted by the new
// spec-aligned routes, Code/Message/Details/Doc are populated from
// the envelope and the raw Body is kept too. Legacy `{"error":"..."}`
// shape only fills Message. Tools branch on Code when present —
// machine-readable beats parsing English.
type HTTPError struct {
	Status  int
	Path    string
	Body    string // raw response body, useful for diagnostics
	Code    string
	Message string
	Details []ErrorDetail
	Doc     string
}

// ErrorDetail mirrors the gateway's ErrorDetail shape (per-field
// validation results). Kept here rather than imported so the mcp
// package stays independent of the daemon package.
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

// ToToolPayload renders the structured fields of an HTTPError into the
// map shape the MCP layer surfaces to the LLM. The status/path/message
// keys are always present; code/details/doc only appear when non-empty
// so the LLM isn't handed empty fields to reason about. errorResultOrErr
// marshals this map into the tool-error text.
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

// parseErrorEnvelope walks the response body looking for the
// structured envelope. Returns zero values when the body doesn't
// parse (then HTTPError.Body alone carries the error text). Tolerant
// of the legacy `{"error":"<string>"}` shape too — fills Message,
// leaves Code/Details empty.
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

// pathSegment percent-encodes a single path segment. Used wherever
// a tenant/workspace/id lands in the URL path — those values come
// from the LLM and could contain anything.
func pathSegment(s string) string {
	return url.PathEscape(s)
}

// buildQuery turns a set of key→value pairs into a "?a=1&b=2" query
// string suffix, skipping empty values entirely. Returns "" when no
// pairs survive, so callers can append it unconditionally. Values are
// URL-encoded via net/url.Values; key order is deterministic (sorted)
// — neither is observable to a conforming server, which decodes the
// params back to their literal values regardless of encoding/order.
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

// composeFlowID encodes the (tenant, workspace, id) triple as the
// composite path parameter the new /me/flows/{flow_id} route
// consumes. Slashes inside the composite become %2F so the value
// stays in a single mux segment on the server.
func composeFlowID(tenant, workspace, id string) string {
	return url.PathEscape(tenant + "/" + workspace + "/" + id)
}

// Get reads a JSON resource at path. The path is appended to /api/v1
// so callers pass "/graphs", not the full URL.
func (c *DazydClient) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// Post sends body and decodes into out (nil out skips decoding). If
// the context carries an idempotency key (set via withIdempotencyKey),
// it ships as `Idempotency-Key` so the gateway can dedupe LLM retries.
func (c *DazydClient) Post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

// Put replaces a resource (graph saves use this).
func (c *DazydClient) Put(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPut, path, body, out)
}

// Patch sends a RFC 7396 JSON Merge Patch. The Content-Type set in `do`
// is generic `application/json`; servers that need the merge-patch
// MIME type to differentiate behaviour should not rely on it. The
// daemon's PATCH /me/flows/{id} handler accepts either.
func (c *DazydClient) Patch(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPatch, path, body, out)
}

// Delete removes a resource. The daemon's DELETE endpoints today
// return 204 with no body; passing nil for `out` is the expected
// shape. Honors Idempotency-Key from context like the other writers.
func (c *DazydClient) Delete(ctx context.Context, path string) error {
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// idempotencyKeyCtx is the typed key for stashing an Idempotency-Key
// value in a request context. Tools wrap the context with
// withIdempotencyKey before calling Post/Patch/Put — the client reads
// it inside `do` and adds the header. Context-based threading keeps
// the public method signatures unchanged.
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
