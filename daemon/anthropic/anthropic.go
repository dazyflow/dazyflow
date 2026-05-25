// Package anthropic is a small streaming client for Anthropic's
// Messages API, scoped to what the in-app chat agent needs: tool use
// and SSE streaming of token deltas. It is deliberately separate
// from integrations/ai/claude.go — that module exists to expose
// single-shot Claude calls as flow NODES, whereas this client drives
// the daemon-side agent loop that *builds* flows.
//
// Why a fresh implementation: the integrations/ai client doesn't do
// streaming or tool_use, and adding either to it would mean carrying
// a much wider surface on a module whose contract is "one prompt in,
// one completion out." Keeping the agent-loop client separate keeps
// each layer's contract small.
//
// API reference:
//
//	https://docs.anthropic.com/en/api/messages
//	https://docs.anthropic.com/en/api/messages-streaming
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	DefaultBaseURL = "https://api.anthropic.com"
	APIVersion     = "2023-06-01"
	DefaultModel   = "claude-sonnet-4-6"
)

// Client is the streaming Messages client. Construct with NewClient
// and reuse — internally it just wraps an http.Client.
type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: 0}, // streaming responses must not be hard-capped
	}
}

// Tool describes a callable function the model can choose to
// invoke. The shape mirrors Anthropic's tool spec verbatim so we
// don't have to translate.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Message is one entry in the Messages.create conversation. Content
// is a json.RawMessage to accommodate the union shape: a plain
// string OR an array of content blocks ({type: "text"|"tool_use"|
// "tool_result", ...}). The agent loop builds these as raw JSON
// because Anthropic accepts both forms and the array form is needed
// for tool_use / tool_result.
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// Request is the body of POST /v1/messages.
type Request struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	System    string    `json:"system,omitempty"`
	MaxTokens int       `json:"max_tokens"`
	Tools     []Tool    `json:"tools,omitempty"`
	Stream    bool      `json:"stream,omitempty"`
}

// Event is one SSE event flowing back. Each callback receives one
// of these; consumers switch on Type. The fields that aren't
// relevant for a given event type are zero.
type Event struct {
	Type string // anthropic event name verbatim (e.g. "content_block_delta")

	// Index identifies which content block this delta belongs to.
	// Used to correlate text_delta / input_json_delta with the
	// matching content_block_start that announced the block.
	Index int

	// Block — set on content_block_start. Tells the consumer
	// whether the block is text or tool_use, and for tool_use the
	// tool name + id so it can prepare a buffer.
	Block *ContentBlock

	// TextDelta — set when Type is content_block_delta and the
	// delta is a text fragment.
	TextDelta string

	// PartialJSON — set when Type is content_block_delta and the
	// delta is incremental tool-input JSON. The consumer
	// concatenates these per-index to reconstruct the full tool
	// input.
	PartialJSON string

	// StopReason — set on message_delta. Values include "end_turn",
	// "tool_use", "max_tokens", "stop_sequence".
	StopReason string

	// Usage — set on message_start (cumulative input usage) and
	// message_delta (final output usage).
	Usage *Usage

	// Error — set when the server reports a streaming error
	// mid-flight.
	Error *APIError
}

// ContentBlock matches the start-of-block shape. tool_use blocks
// carry id + name; the input field stays empty here and is filled
// from input_json_delta events the streaming layer accumulates per
// index for the caller.
type ContentBlock struct {
	Type string `json:"type"`
	// text-type blocks have no extra metadata at start.
	// tool_use:
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

// APIError mirrors Anthropic's error envelope.
type APIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("anthropic %s: %s", e.Type, e.Message)
}

// Stream runs a streaming Messages request, invoking onEvent for
// each parsed SSE event. Returns when the server closes the stream
// or an error occurs. onEvent's error short-circuits the stream so
// the caller can cancel mid-response (e.g. abort signal from the UI).
func (c *Client) Stream(ctx context.Context, req Request, onEvent func(Event) error) error {
	if c.APIKey == "" {
		return fmt.Errorf("anthropic: no API key configured")
	}
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	req.Stream = true
	if req.Model == "" {
		req.Model = DefaultModel
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 4096
	}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+"/v1/messages",
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("x-api-key", c.APIKey)
	httpReq.Header.Set("anthropic-version", APIVersion)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return fmt.Errorf("anthropic POST: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Non-200 responses arrive as plain JSON, not SSE. Parse
		// what we can; fall back to raw body if it isn't the
		// expected envelope.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		var env struct {
			Error APIError `json:"error"`
			Type  string   `json:"type"`
		}
		if jerr := json.Unmarshal(raw, &env); jerr == nil && env.Error.Message != "" {
			return fmt.Errorf("anthropic %d: %w", resp.StatusCode, &env.Error)
		}
		return fmt.Errorf("anthropic %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	return parseSSE(resp.Body, onEvent)
}

// parseSSE walks the Server-Sent Events stream. The format is
//
//	event: <name>
//	data: <json line>
//	\n
//
// Anthropic always pairs event+data on consecutive lines and
// terminates each event with a blank line. We tolerate the
// `id:`/`retry:` lines the spec allows but Anthropic doesn't use,
// just by ignoring unrecognized prefixes.
func parseSSE(r io.Reader, onEvent func(Event) error) error {
	scanner := bufio.NewScanner(r)
	// Anthropic events can be large (a full tool_use block input
	// arrives as input_json_delta fragments but a buffered server
	// can flush one huge content_block_start). 4MB keeps headroom.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var eventName string
	var dataLines []string

	flush := func() error {
		defer func() {
			eventName = ""
			dataLines = dataLines[:0]
		}()
		if eventName == "" && len(dataLines) == 0 {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		// Anthropic uses event names like "message_start",
		// "content_block_start", "content_block_delta",
		// "content_block_stop", "message_delta", "message_stop",
		// "ping", "error".
		switch eventName {
		case "", "ping":
			return nil
		case "error":
			var env struct {
				Error APIError `json:"error"`
			}
			if err := json.Unmarshal([]byte(data), &env); err == nil {
				return onEvent(Event{Type: "error", Error: &env.Error})
			}
			return fmt.Errorf("anthropic streaming error: %s", data)
		}
		return dispatchSSEEvent(eventName, data, onEvent)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimSpace(line[len("data:"):]), ""))
		default:
			// id:, retry:, comments — ignore.
		}
	}
	// Final partial event if the server skipped the trailing
	// blank line (rare but allowed).
	if err := flush(); err != nil {
		return err
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}

// dispatchSSEEvent decodes one event's data line into the typed
// Event shape and hands it to the caller. The structures used for
// decoding are local — they exist only to peel the wire shape.
func dispatchSSEEvent(name, data string, onEvent func(Event) error) error {
	switch name {
	case "message_start":
		var m struct {
			Message struct {
				Usage Usage `json:"usage"`
			} `json:"message"`
		}
		_ = json.Unmarshal([]byte(data), &m)
		return onEvent(Event{Type: name, Usage: &m.Message.Usage})

	case "content_block_start":
		var m struct {
			Index        int          `json:"index"`
			ContentBlock ContentBlock `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			return fmt.Errorf("content_block_start: %w", err)
		}
		return onEvent(Event{Type: name, Index: m.Index, Block: &m.ContentBlock})

	case "content_block_delta":
		var m struct {
			Index int `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text,omitempty"`
				PartialJSON string `json:"partial_json,omitempty"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			return fmt.Errorf("content_block_delta: %w", err)
		}
		ev := Event{Type: name, Index: m.Index}
		switch m.Delta.Type {
		case "text_delta":
			ev.TextDelta = m.Delta.Text
		case "input_json_delta":
			ev.PartialJSON = m.Delta.PartialJSON
		}
		return onEvent(ev)

	case "content_block_stop":
		var m struct {
			Index int `json:"index"`
		}
		_ = json.Unmarshal([]byte(data), &m)
		return onEvent(Event{Type: name, Index: m.Index})

	case "message_delta":
		var m struct {
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage Usage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			return fmt.Errorf("message_delta: %w", err)
		}
		return onEvent(Event{Type: name, StopReason: m.Delta.StopReason, Usage: &m.Usage})

	case "message_stop":
		return onEvent(Event{Type: name})
	}
	// Forward unknown events with the name preserved so callers
	// see them in logs even if we don't model them.
	return onEvent(Event{Type: name})
}

