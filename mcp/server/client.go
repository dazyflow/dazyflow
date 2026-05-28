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

// HazydClient is a thin HTTP client over the hzd /api/v1 gateway. It
// is intentionally minimal — the MCP tools that wrap it do all the
// shape massaging; the client just signs requests and parses
// responses.
//
// Auth is bearer-token, supplied via $HAZYFLOW_API_KEY in the MCP
// client config. The token is never logged.
type HazydClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewHazydClient builds a client against baseURL with token, using a
// 30s default HTTP timeout. Callers can override .HTTP for tests.
func NewHazydClient(baseURL, token string) *HazydClient {
	return &HazydClient{
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

func (c *HazydClient) Whoami(ctx context.Context) (Whoami, error) {
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
func (c *HazydClient) do(ctx context.Context, method, path string, body, out any) error {
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
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("hazy-flow %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{
			Status: resp.StatusCode,
			Body:   strings.TrimSpace(string(respBody)),
			Path:   method + " " + path,
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
type HTTPError struct {
	Status int
	Body   string
	Path   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s: %d %s", e.Path, e.Status, e.Body)
}

// pathSegment percent-encodes a single path segment. Used wherever
// a tenant/workspace/id lands in the URL path — those values come
// from the LLM and could contain anything.
func pathSegment(s string) string {
	return url.PathEscape(s)
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
func (c *HazydClient) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// Post sends body and decodes into out (nil out skips decoding).
func (c *HazydClient) Post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

// Put is used for graph saves (PUT /graphs/{t}/{w}/{id}).
func (c *HazydClient) Put(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPut, path, body, out)
}
