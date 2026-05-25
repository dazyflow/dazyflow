package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

// The parser must skip system / rate_limit / unknown frames, surface
// assistant text deltas, dispatch tool_use as tool_use_start, and
// emit a single "done" on result. Fixture lines are from a real
// claude -p run (trimmed for brevity).
func TestParseClaudeCLIStream_HappyPath(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","tools":["Bash"]}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"I'll list available drops."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu_1","name":"mcp__hazy-flow__list_drops","input":{}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu_1","content":"{\"http_request\":{}}"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Now proposing a flow."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu_2","name":"mcp__hazy-flow__create_flow","input":{"id":"new","nodes":[{"id":"a","module":"noop"}]}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu_2","content":"{\"commit\":\"abc\"}"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","result":"Done."}`,
	}, "\n") + "\n"

	var events []ChatEvent
	err := parseClaudeCLIStream(strings.NewReader(stream), func(ev ChatEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Expected sequence: 2 text, 2 tool_use_start, 1 proposal (for
	// create_flow), 2 tool_use_result, 1 done.
	wantTypes := []string{
		"text",
		"tool_use_start",
		"tool_use_result",
		"text",
		"tool_use_start",
		"proposal",
		"tool_use_result",
		"done",
	}
	gotTypes := make([]string, 0, len(events))
	for _, e := range events {
		gotTypes = append(gotTypes, e.Type)
	}
	if strings.Join(gotTypes, ",") != strings.Join(wantTypes, ",") {
		t.Fatalf("types = %v, want %v", gotTypes, wantTypes)
	}

	// Spot-check the proposal — the input graph must round-trip into
	// ChatEvent.Proposal verbatim.
	var prop *ChatEvent
	for i := range events {
		if events[i].Type == "proposal" {
			prop = &events[i]
		}
	}
	if prop == nil || prop.Proposal == nil || prop.Proposal.ID != "new" {
		t.Fatalf("proposal = %+v", prop)
	}
}

// Tool results sometimes arrive as content-block arrays rather than
// plain strings. The stringify helper must surface the text payload
// either way so the UI doesn't show "[object Object]".
func TestStringifyToolResult_Variants(t *testing.T) {
	cases := map[string]any{
		"hello":                                "hello",
		"slice of text blocks reduces to text": []any{map[string]any{"type": "text", "text": "slice of text blocks reduces to text"}},
	}
	for want, in := range cases {
		got := stringifyToolResult(in)
		if got != want {
			t.Errorf("stringify(%v) = %q, want %q", in, got, want)
		}
	}
}

// A result frame with is_error=true must yield a single "error"
// ChatEvent — anything else (silently flipping to done) would
// hide the failure from the UI.
func TestParseClaudeCLIStream_ErrorResult(t *testing.T) {
	stream := `{"type":"result","subtype":"success","is_error":true,"result":"auth failed"}` + "\n"

	var got []ChatEvent
	_ = parseClaudeCLIStream(strings.NewReader(stream), func(ev ChatEvent) error {
		got = append(got, ev)
		return nil
	})
	if len(got) != 1 || got[0].Type != "error" {
		t.Fatalf("events = %+v", got)
	}
	if !strings.Contains(got[0].ErrorText, "auth failed") {
		t.Errorf("error text = %q", got[0].ErrorText)
	}
}

// The build prompt should fold a multi-turn transcript into the
// "ROLE: text" shape and end with "ASSISTANT:" so claude knows
// where to start. Belt-and-braces against rebuilds that might
// accidentally drop the trailing marker.
func TestBuildClaudeCLIPrompt_Transcript(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: json.RawMessage(`"build a flow"`)},
		{Role: "assistant", Content: json.RawMessage(`"sure, what kind?"`)},
		{Role: "user", Content: json.RawMessage(`"slack notifier"`)},
	}
	got := buildClaudeCLIPrompt(msgs)
	if !strings.Contains(got, "USER: build a flow") {
		t.Errorf("missing user 1: %q", got)
	}
	if !strings.Contains(got, "ASSISTANT: sure, what kind?") {
		t.Errorf("missing assistant turn: %q", got)
	}
	if !strings.HasSuffix(got, "ASSISTANT: ") {
		t.Errorf("must end with ASSISTANT: prompt cue; got %q", got)
	}
}
