// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mcp implements the Model Context Protocol client and the Dazy
// Flow transport built on it. MCP is Anthropic's open protocol for
// connecting LLM tools and resources; spec at
// https://spec.modelcontextprotocol.io/.
//
// This package handles the stdio transport (server runs as subprocess,
// newline-delimited JSON-RPC 2.0 over stdin/stdout). HTTP+SSE is a
// future addition with the same client shape.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Client speaks JSON-RPC 2.0 over a pair of streams. Calls are
// synchronous from the caller's perspective; the write mutex serializes
// outbound writes, and a single reader goroutine dispatches inbound
// responses to the matching call by ID.
type Client struct {
	w  io.Writer
	wm sync.Mutex
	r  *bufio.Reader

	nextID atomic.Int64

	pmu     sync.Mutex
	pending map[int64]chan rawResponse
	closed  bool

	readerDone chan struct{}
	readErr    atomic.Pointer[error]
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rawResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError mirrors the JSON-RPC 2.0 error object. MCP servers report
// tool failures as error responses with codes; clients surface those to
// the caller.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message)
}

// NewClient wires a reader+writer pair to a freshly-started reader
// goroutine. The caller is responsible for closing the underlying
// connection (in stdio case, killing the subprocess) when done; this
// triggers the reader to drain and fail any pending calls.
func NewClient(w io.Writer, r io.Reader) *Client {
	c := &Client{
		w:          w,
		r:          bufio.NewReader(r),
		pending:    make(map[int64]chan rawResponse),
		readerDone: make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *Client) readLoop() {
	defer close(c.readerDone)
	for {
		line, err := c.r.ReadBytes('\n')
		if len(line) > 0 {
			c.dispatch(line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.readErr.Store(&err)
			}
			c.failAllPending()
			return
		}
	}
}

func (c *Client) dispatch(line []byte) {
	var resp rawResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		// Server-initiated notifications (no id) and malformed lines
		// both land here. Ignore — we don't model server→client
		// notifications yet.
		return
	}
	if resp.ID == 0 {
		return
	}
	c.pmu.Lock()
	ch, ok := c.pending[resp.ID]
	if ok {
		delete(c.pending, resp.ID)
	}
	c.pmu.Unlock()
	if ok {
		ch <- resp
		close(ch)
	}
}

func (c *Client) failAllPending() {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	for _, ch := range c.pending {
		close(ch)
	}
	c.pending = nil
	c.closed = true
}

// Call sends a JSON-RPC request and blocks until the matching response
// arrives, the context is cancelled, or the connection closes. result
// is JSON-unmarshalled from the response's result field; pass nil to
// ignore it.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)
	ch := make(chan rawResponse, 1)

	c.pmu.Lock()
	if c.closed {
		c.pmu.Unlock()
		return errors.New("mcp: connection closed")
	}
	c.pending[id] = ch
	c.pmu.Unlock()

	req := request{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		c.discardPending(id)
		return fmt.Errorf("marshal %q: %w", method, err)
	}
	data = append(data, '\n')

	c.wm.Lock()
	_, werr := c.w.Write(data)
	c.wm.Unlock()
	if werr != nil {
		c.discardPending(id)
		return fmt.Errorf("write %q: %w", method, werr)
	}

	select {
	case <-ctx.Done():
		c.discardPending(id)
		return ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return fmt.Errorf("mcp: connection closed during %q", method)
		}
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	}
}

// Notify sends a fire-and-forget notification (no response expected).
// Used for initialized/cancellation/progress messages.
func (c *Client) Notify(method string, params any) error {
	n := notification{JSONRPC: "2.0", Method: method, Params: params}
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	c.wm.Lock()
	_, werr := c.w.Write(data)
	c.wm.Unlock()
	return werr
}

func (c *Client) discardPending(id int64) {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	delete(c.pending, id)
}
