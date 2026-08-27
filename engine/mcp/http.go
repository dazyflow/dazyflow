// SPDX-FileCopyrightText: 2026 Joachim Klahr
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
// single JSON object or an SSE stream carrying the response among other
// events.
//
// This is the transport a TENANT may configure, and the stdio one is not. The
// difference is not a preference. RegisterStdio spawns a subprocess on the
// daemon host, so exposing it to an org admin would make "add an MCP server"
// a way to run commands as the daemon user — on a multi-tenant instance, from
// any org. Nothing here starts a process: a tenant server is a URL the daemon
// makes requests to, which is a capability an org already has via http_request.

// maxHTTPResponseBytes caps what one response may hand back. A remote MCP
// server is a third party a tenant chose, not one we trust: without a cap, a
// server answering an endless body would grow the daemon's heap until it dies,
// taking every other tenant's runs with it.
const maxHTTPResponseBytes = 8 << 20

// httpNotifyTimeout bounds a fire-and-forget notification. Notify carries no
// context (the stdio transport's writes are local and never block), so the
// HTTP one supplies its own rather than dialing with no deadline at all.
const httpNotifyTimeout = 30 * time.Second

// DialControl is a net.Dialer Control hook: it sees each connection attempt
// after DNS resolution and may refuse it.
type DialControl = func(network, address string, c syscall.RawConn) error

// dialControl holds the SSRF guard applied to every HTTP MCP server.
//
// Injected rather than implemented here, and rather than imported: the guard
// lives in drops/net, which imports engine — so engine/mcp importing it back
// is a cycle. cmd/dzd wires net.SSRFDialControl() in at startup, the same hook
// pattern SetEgressPolicy uses.
//
// Unset means no guard, which is right for a unit test dialing its own
// httptest server on loopback and wrong for a daemon. cmd/dzd always sets it.
var dialControl atomic.Pointer[DialControl]

// SetDialControl installs the dial guard used for HTTP MCP servers. Passing
// nil clears it.
func SetDialControl(fn DialControl) {
	if fn == nil {
		dialControl.Store(nil)
		return
	}
	dialControl.Store(&fn)
}

// HTTPDescriptor names a remote MCP server reachable over HTTP.
type HTTPDescriptor struct {
	// Name is the server identifier used in tool IDs (mcp:<name>:<tool>).
	Name string
	// Tenant owning this server. Empty means instance-wide — the operator's
	// own servers, visible to every org. A tenant's server is reachable ONLY
	// by that tenant: the catalog is keyed by (tenant, id), so a lookup for
	// another org cannot return it even by mistake.
	//
	// This matters for the same reason it matters on a remote runner: by the
	// time the engine hands a Job to a transport, Params carry RESOLVED
	// secrets. A server any tenant could reach would be a place one org's
	// credentials could be sent by another org's flow.
	Tenant string
	// URL is the server's single MCP endpoint.
	URL string
	// Header carries auth (Authorization: Bearer …, or a vendor's own header).
	// Copied at construction, so a later mutation by the caller cannot change
	// what a live connection sends.
	Header http.Header
	// HTTPClient overrides the constructed client. Tests set it; production
	// leaves it nil so the SSRF guard and timeouts below apply.
	HTTPClient *http.Client
	// Timeout bounds one request. Zero means defaultHTTPTimeout.
	Timeout time.Duration
}

const defaultHTTPTimeout = 60 * time.Second

// HTTPClient speaks JSON-RPC 2.0 over streamable HTTP.
//
// Unlike the stdio Client there is no reader goroutine and no pending-call
// table: a request and its response are one HTTP round trip, so the
// correlation the stdio transport needs a map for is the connection itself.
type HTTPClient struct {
	url    string
	hc     *http.Client
	header http.Header

	nextID atomic.Int64

	// mu guards sessionID, which the server assigns on initialize and every
	// later request must echo.
	mu        sync.Mutex
	sessionID string
}

// NewHTTPClient builds a client for the descriptor. It performs no I/O — the
// handshake is RegisterHTTP's job.
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

// buildHTTPClient installs the SSRF guard at dial time. The Control hook fires
// on each connection attempt AFTER DNS resolution, so a hostname that resolves
// to 169.254.169.254 is refused just as a literal one would be — which is the
// property a pre-flight hostname check cannot offer.
//
// No Client.Timeout: it would bound the whole exchange including the body,
// and a tool call legitimately running for minutes would be cut off mid-SSRF.
// The per-request context carries the deadline instead, with the transport's
// own timeouts bounding the parts that must never hang.
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
		// An MCP endpoint that redirects is not something the spec describes,
		// and following one would re-dial a host the guard already vetted for
		// a DIFFERENT URL. Refuse instead of quietly ending up elsewhere.
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return fmt.Errorf("mcp endpoint redirected to %s; point the server URL at its final address", req.URL)
		},
	}
}

// SessionID is the session the server assigned at initialize, if any. Exposed
// for diagnostics — an admin looking at why a server stopped answering wants
// to know whether a session was ever established.
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

// Call sends one JSON-RPC request and decodes its response into result.
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

	// initialize is where the server hands out a session id; later requests
	// must echo it. Captured on every response rather than only that one,
	// because a server is permitted to (re)assign it.
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

// Notify sends a JSON-RPC notification — no id, and no response expected. A
// conforming server answers 202 with an empty body.
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
	// The body is not read: a notification has no response, and some servers
	// answer 200 with an empty stream rather than 202. Draining a stream that
	// may never end is the one thing not to do here.
	return checkHTTPStatus(resp)
}

// Close releases pooled connections. There is no session to tear down beyond
// that: DELETE-ing the session is optional in the spec and a server that
// ignores it would leave Close reporting a failure for a connection that is
// gone either way.
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
	// Both are advertised because a server chooses: a small result comes back
	// as one JSON object, a long-running call as an SSE stream.
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

// checkHTTPStatus turns a non-2xx into an error carrying enough of the body to
// diagnose it — a 401 from a server whose token expired is the single most
// likely failure, and "unexpected status 401" alone would not say so.
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
		// 404 on a session-carrying request is how the spec says a session
		// expired, and it is indistinguishable here from a wrong URL. Say both.
		return fmt.Errorf("mcp endpoint answered 404 — wrong URL, or the session expired%s", detail)
	default:
		return fmt.Errorf("mcp server returned HTTP %d%s", resp.StatusCode, detail)
	}
}

// readRPCResponse pulls the response for id out of either body shape.
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

// readSSEResponse scans an SSE stream for the response to id.
//
// Other events pass through unread on purpose: a server may interleave
// progress notifications and log messages with the answer, and none of them
// are what the caller is waiting for. The stream is read only until the
// matching response arrives — a server that keeps the connection open
// afterwards does not keep this call open with it.
func readSSEResponse(body io.Reader, id int64) (*rawResponse, error) {
	scanner := bufio.NewScanner(body)
	// SSE lines carry whole JSON-RPC messages, which for a tool result can be
	// large; the default 64KiB token limit would fail on an ordinary answer.
	scanner.Buffer(make([]byte, 0, 64*1024), maxHTTPResponseBytes)

	var data strings.Builder
	// dispatch parses one accumulated event and reports whether it was the
	// response we came for.
	dispatch := func() (*rawResponse, bool) {
		if data.Len() == 0 {
			return nil, false
		}
		payload := data.String()
		data.Reset()
		var raw rawResponse
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			// A malformed or non-JSON-RPC event (a keepalive comment, a
			// server's own log line) is skipped rather than failing the call:
			// the response may still be coming.
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
			// Comment / keepalive.
		case strings.HasPrefix(line, "data:"):
			// Multi-line data fields concatenate with newlines, per SSE.
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// event:, id:, retry: — framing we do not need.
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read event stream: %w", err)
	}
	// A stream can end without a trailing blank line.
	if raw, ok := dispatch(); ok {
		return raw, nil
	}
	return nil, fmt.Errorf("event stream ended without a response to request %d", id)
}
