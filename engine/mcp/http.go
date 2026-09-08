// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	stdnet "net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// The streamable-HTTP transport: one endpoint, POSTed to, answering either a
// single JSON object or an SSE stream carrying the response among other events.
//
// This is the transport a TENANT may configure, and the stdio one is not.
// RegisterStdio spawns a subprocess on the daemon host, so exposing it to an org
// admin would make "add an MCP server" a way to run commands as the daemon user,
// from any org. Nothing here starts a process: a tenant server is a URL the
// daemon makes requests to, which an org already has via http_request.

// maxHTTPResponseBytes: a remote MCP server is a third party a tenant chose, not
// one we trust, and an endless body would grow the daemon's heap until it dies,
// taking every other tenant's runs with it.
const maxHTTPResponseBytes = 8 << 20

// httpNotifyTimeout: Notify carries no context — the stdio transport's writes
// are local and never block — so the HTTP one supplies its own rather than
// dialing with no deadline.
const httpNotifyTimeout = 30 * time.Second

type DialControl = func(network, address string, c syscall.RawConn) error

// dialControl is the SSRF guard for every HTTP MCP server. Injected rather than
// imported: the guard lives in drops/net, which imports engine, so importing it
// back is a cycle. Unset means no guard, which is right for a unit test on
// loopback and wrong for a daemon; cmd/dzd always sets it.
var dialControl atomic.Pointer[DialControl]

func SetDialControl(fn DialControl) {
	if fn == nil {
		dialControl.Store(nil)
		return
	}
	dialControl.Store(&fn)
}

type HTTPDescriptor struct {
	Name  string
	Label string
	// Tenant owns this server; empty means instance-wide. The catalog is keyed by
	// (tenant, id), so another org's lookup cannot return it even by mistake — which
	// matters because by the time the engine hands a Job to a transport, Params carry
	// RESOLVED secrets.
	Tenant string
	URL    string
	// Header carries auth, copied at construction so a later mutation cannot change
	// what a live connection sends.
	Header     http.Header
	HTTPClient *http.Client
	Timeout    time.Duration
}

const defaultHTTPTimeout = 60 * time.Second

// HTTPClient needs no reader goroutine and no pending-call table: a request and
// its response are one round trip, so the connection itself is the correlation
// the stdio transport needs a map for.
type HTTPClient struct {
	url    string
	hc     *http.Client
	header http.Header

	nextID atomic.Int64

	// mu guards sessionID, which the server assigns on initialize and every later
	// request must echo.
	mu        sync.Mutex
	sessionID string
}

func NewHTTPClient(desc HTTPDescriptor) *HTTPClient {
	timeout := desc.Timeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	hc := desc.HTTPClient
	if hc == nil {
		hc = buildHTTPClient(timeout)
	}
	header := http.Header{}
	for k, vs := range desc.Header {
		for _, v := range vs {
			header.Add(k, v)
		}
	}
	return &HTTPClient{url: desc.URL, hc: hc, header: header}
}

// buildHTTPClient installs the guard at dial time, AFTER DNS resolution, so a
// hostname resolving to 169.254.169.254 is refused just as a literal one is —
// the property a pre-flight hostname check cannot offer.
//
// No Client.Timeout: it would bound the whole exchange including the body, cutting
// off a tool call legitimately running for minutes. The per-request context
// carries the deadline instead.
func buildHTTPClient(timeout time.Duration) *http.Client {
	dialer := &stdnet.Dialer{Timeout: timeout}
	if fn := dialControl.Load(); fn != nil {
		dialer.Control = *fn
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			MaxIdleConns:          4,
			IdleConnTimeout:       60 * time.Second,
		},
		// Following a redirect would re-dial a host the guard vetted for a DIFFERENT
		// URL.
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return fmt.Errorf("mcp endpoint redirected to %s; point the server URL at its final address", req.URL)
		},
	}
}

func (c *HTTPClient) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

func (c *HTTPClient) Initialize(ctx context.Context, name, version string) (*InitializeResult, error) {
	return initialize(ctx, c, httpProtocolVersion, name, version)
}

func (c *HTTPClient) ListTools(ctx context.Context) ([]Tool, error) { return listTools(ctx, c) }

func (c *HTTPClient) CallTool(ctx context.Context, name string, args map[string]any) (*ToolCallResult, error) {
	return callTool(ctx, c, name, args)
}

func (c *HTTPClient) Call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)
	body, err := json.Marshal(request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("marshal %s: %w", method, err)
	}
	resp, err := c.post(ctx, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.mu.Lock()
		c.sessionID = sid
		c.mu.Unlock()
	}

	if err := checkHTTPStatus(resp); err != nil {
		return err
	}

	raw, err := readRPCResponse(resp, id)
	if err != nil {
		return err
	}
	if raw.Error != nil {
		return raw.Error
	}
	if result == nil || len(raw.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw.Result, result); err != nil {
		return fmt.Errorf("decode %s result: %w", method, err)
	}
	return nil
}

func (c *HTTPClient) Notify(method string, params any) error {
	body, err := json.Marshal(notification{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("marshal %s: %w", method, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), httpNotifyTimeout)
	defer cancel()
	resp, err := c.post(ctx, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.mu.Lock()
		c.sessionID = sid
		c.mu.Unlock()
	}
	// The body is not read: some servers answer 200 with an empty stream rather
	// than 202, and draining a stream that may never end is the one thing not to do.
	return checkHTTPStatus(resp)
}

// Close releases pooled connections. DELETE-ing the session is optional in the
// spec, and a server ignoring it would make Close report a failure for a
// connection that is gone either way.
func (c *HTTPClient) Close() error {
	c.hc.CloseIdleConnections()
	return nil
}

func (c *HTTPClient) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for k, vs := range c.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", httpProtocolVersion)
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp http: %w", err)
	}
	return resp, nil
}

// checkHTTPStatus carries enough of the body to diagnose it: a 401 from a server
// whose token expired is the likeliest failure, and "unexpected status 401" alone
// would not say so.
func checkHTTPStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	detail := strings.TrimSpace(string(snippet))
	if detail != "" {
		detail = ": " + detail
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("mcp server refused the credential (HTTP %d)%s", resp.StatusCode, detail)
	case http.StatusNotFound:
		// A 404 on a session-carrying request is how the spec says a session expired,
		// and it is indistinguishable here from a wrong URL. Say both.
		return fmt.Errorf("mcp endpoint answered 404 — wrong URL, or the session expired%s", detail)
	default:
		return fmt.Errorf("mcp server returned HTTP %d%s", resp.StatusCode, detail)
	}
}

func readRPCResponse(resp *http.Response, id int64) (*rawResponse, error) {
	body := io.LimitReader(resp.Body, maxHTTPResponseBytes)
	if isEventStream(resp.Header.Get("Content-Type")) {
		return readSSEResponse(body, id)
	}
	var raw rawResponse
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if raw.ID != id {
		return nil, fmt.Errorf("response id %d does not match request %d", raw.ID, id)
	}
	return &raw, nil
}

func isEventStream(contentType string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(contentType)), "text/event-stream")
}

func readSSEResponse(body io.Reader, id int64) (*rawResponse, error) {
	scanner := bufio.NewScanner(body)
	// SSE lines carry whole JSON-RPC messages, so the default 64KiB token limit
	// would fail on an ordinary tool result.
	scanner.Buffer(make([]byte, 0, 64*1024), maxHTTPResponseBytes)

	var data strings.Builder
	dispatch := func() (*rawResponse, bool) {
		if data.Len() == 0 {
			return nil, false
		}
		payload := data.String()
		data.Reset()
		var raw rawResponse
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return nil, false
		}
		if raw.ID != id {
			return nil, false
		}
		return &raw, true
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if raw, ok := dispatch(); ok {
				return raw, nil
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read event stream: %w", err)
	}
	if raw, ok := dispatch(); ok {
		return raw, nil
	}
	return nil, fmt.Errorf("event stream ended without a response to request %d", id)
}
