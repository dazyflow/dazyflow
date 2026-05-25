package anthropic_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/daemon/anthropic"
)

// Verifies the streaming parser handles the canonical Anthropic
// event sequence: message_start → content_block_start (text) →
// content_block_delta×N → content_block_stop → content_block_start
// (tool_use) → input_json_delta×N → content_block_stop →
// message_delta (stop_reason=tool_use) → message_stop. The shape
// here is verbatim from a captured response.
func TestStream_TextThenToolUse(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"m_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":42,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":", world"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"list_drops","input":{}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"tenant\":"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"dev\"}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":17}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	c := anthropic.NewClient("test")
	c.BaseURL = srv.URL

	var (
		gotText       strings.Builder
		toolName      string
		toolID        string
		toolInput     strings.Builder
		stopReason    string
	)
	err := c.Stream(t.Context(), anthropic.Request{
		Model:    "claude-sonnet-4-6",
		Messages: []anthropic.Message{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}, func(ev anthropic.Event) error {
		switch ev.Type {
		case "content_block_start":
			if ev.Block != nil && ev.Block.Type == "tool_use" {
				toolName = ev.Block.Name
				toolID = ev.Block.ID
			}
		case "content_block_delta":
			if ev.TextDelta != "" {
				gotText.WriteString(ev.TextDelta)
			}
			if ev.PartialJSON != "" {
				toolInput.WriteString(ev.PartialJSON)
			}
		case "message_delta":
			stopReason = ev.StopReason
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if gotText.String() != "Hello, world" {
		t.Errorf("text = %q", gotText.String())
	}
	if toolName != "list_drops" || toolID != "toolu_1" {
		t.Errorf("tool = %q (id=%q)", toolName, toolID)
	}
	if toolInput.String() != `{"tenant":"dev"}` {
		t.Errorf("tool input = %q", toolInput.String())
	}
	if stopReason != "tool_use" {
		t.Errorf("stop_reason = %q", stopReason)
	}
}

// A non-200 response from Anthropic arrives as plain JSON, not SSE.
// The client must surface a typed error so callers can decide
// whether to retry — silently dropping a 401 here would let the
// agent loop spin forever waiting on tokens that never come.
func TestStream_NonOKReturnsTypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	}))
	t.Cleanup(srv.Close)

	c := anthropic.NewClient("bad")
	c.BaseURL = srv.URL

	err := c.Stream(t.Context(), anthropic.Request{
		Messages: []anthropic.Message{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}, func(anthropic.Event) error { return nil })
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Errorf("error = %v", err)
	}
}

func TestStream_OnEventErrorStopsStream(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"model":"x","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
	}, "\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	c := anthropic.NewClient("test")
	c.BaseURL = srv.URL

	stopErr := io.EOF // sentinel — content irrelevant
	err := c.Stream(t.Context(), anthropic.Request{
		Messages: []anthropic.Message{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}, func(ev anthropic.Event) error {
		if ev.Type == "content_block_start" {
			return stopErr
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected error from short-circuit")
	}
}

// Sanity: missing API key is a hard config error, not a wire call.
func TestStream_NoAPIKey(t *testing.T) {
	c := anthropic.NewClient("")
	err := c.Stream(context.Background(), anthropic.Request{
		Messages: []anthropic.Message{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}, func(anthropic.Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("err = %v", err)
	}
}
