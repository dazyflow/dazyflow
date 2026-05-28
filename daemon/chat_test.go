package daemon_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/daemon"
)

// fakeAnthropic stages a sequence of SSE responses — one per
// expected POST to /v1/messages. The chat agent's loop sends one
// request per turn, so we feed it ordered scripts: first response
// makes the model call propose_flow; second response is the
// post-tool turn that ends the conversation.
func fakeAnthropic(t *testing.T, scripts ...string) *httptest.Server {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls >= len(scripts) {
			http.Error(w, "no more scripted responses", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, scripts[calls])
		calls++
	}))
	t.Cleanup(srv.Close)
	return srv
}

// sse builds a JSON-RPC SSE stream from event/data pairs. Tests
// stay readable when the script is one slice of (event, data) lines.
func sse(parts ...string) string {
	var b strings.Builder
	for i := 0; i < len(parts); i += 2 {
		b.WriteString("event: ")
		b.WriteString(parts[i])
		b.WriteByte('\n')
		b.WriteString("data: ")
		b.WriteString(parts[i+1])
		b.WriteString("\n\n")
	}
	return b.String()
}

// TestChatStream_ProposalRoundTrip runs the full agent loop end to
// end against a scripted Anthropic. The model emits a propose_flow
// tool_use; the daemon executes it (validating the graph, NOT
// saving), forwards the proposal event, then receives a
// second-turn response with stop_reason=end_turn. The harness
// collects every ChatEvent the server emitted and asserts on shape.
func TestChatStream_ProposalRoundTrip(t *testing.T) {
	h := newVisibilityHarness(t)

	proposal := `{"id":"new-flow","nodes":[{"id":"a","module":"noop","params":{}}],"edges":[]}`
	turn1 := sse(
		"message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","content":[],"model":"x","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
		"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Proposing a flow."}}`,
		"content_block_stop", `{"type":"content_block_stop","index":0}`,
		"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu1","name":"propose_flow","input":{}}}`,
		"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":`+jsonString(proposal)+`}}`,
		"content_block_stop", `{"type":"content_block_stop","index":1}`,
		"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
		"message_stop", `{"type":"message_stop"}`,
	)
	turn2 := sse(
		"message_start", `{"type":"message_start","message":{"id":"m2","type":"message","role":"assistant","content":[],"model":"x","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
		"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done — review and apply."}}`,
		"content_block_stop", `{"type":"content_block_stop","index":0}`,
		"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		"message_stop", `{"type":"message_stop"}`,
	)
	api := fakeAnthropic(t, turn1, turn2)
	h.svc.EncryptedSecrets = newTestEncryptedSecrets(t)
	if err := h.svc.EncryptedSecrets.Put(context.Background(), h.alice.Tenant, daemon.TenantAnthropicKeyName, "test-key"); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	h.svc.AnthropicBaseURL = api.URL

	var (
		events []daemon.ChatEvent
		text   strings.Builder
	)
	err := h.svc.ChatStream(context.Background(), h.alice, "",
		[]daemon.ChatMessage{{Role: "user", Content: json.RawMessage(`"build me a flow"`)}},
		func(ev daemon.ChatEvent) error {
			events = append(events, ev)
			if ev.Type == "text" {
				text.WriteString(ev.Delta)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	wantPrefix := "Proposing a flow."
	if !strings.HasPrefix(text.String(), wantPrefix) {
		t.Errorf("text = %q", text.String())
	}

	// Find the proposal event and assert it carries the validated
	// graph with the principal's tenant/workspace stamped on it.
	var prop *daemon.ChatEvent
	for i := range events {
		if events[i].Type == "proposal" {
			prop = &events[i]
		}
	}
	if prop == nil {
		t.Fatalf("no proposal event in %d events", len(events))
	}
	if prop.Proposal == nil || prop.Proposal.ID != "new-flow" {
		t.Errorf("proposal = %+v", prop.Proposal)
	}
	if prop.Proposal.Tenant != "t" || prop.Proposal.Workspace != "ws" {
		t.Errorf("proposal scope = %q/%q, want forced to principal", prop.Proposal.Tenant, prop.Proposal.Workspace)
	}

	// Final event must be done with end_turn — without it the UI
	// doesn't know when to re-enable the input box.
	last := events[len(events)-1]
	if last.Type != "done" || last.StopReason != "end_turn" {
		t.Errorf("final event = %+v", last)
	}
}

// Missing per-tenant key: chat should emit a structured error event
// with code "anthropic_key_missing" so the UI can route the user to
// chat settings, and short-circuit before any wire traffic.
func TestChatStream_RequiresAPIKey(t *testing.T) {
	h := newVisibilityHarness(t)
	h.svc.EncryptedSecrets = newTestEncryptedSecrets(t) // store on, key not set

	var events []daemon.ChatEvent
	err := h.svc.ChatStream(context.Background(), h.alice, "",
		[]daemon.ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		func(ev daemon.ChatEvent) error {
			events = append(events, ev)
			return nil
		})
	if err != nil {
		t.Fatalf("ChatStream returned err = %v; missing-key should surface via event, not return", err)
	}
	if len(events) != 1 || events[0].Type != "error" {
		t.Fatalf("events = %+v, want a single error event", events)
	}
	if events[0].ErrorCode != "anthropic_key_missing" {
		t.Errorf("error code = %q, want anthropic_key_missing", events[0].ErrorCode)
	}
}

// Operator hasn't enabled the encrypted store at all: chat fails with
// code "encrypted_store_off" so the UI can point at the operator.
func TestChatStream_RequiresEncryptedStore(t *testing.T) {
	h := newVisibilityHarness(t)
	// EncryptedSecrets stays nil — operator never set --master-key.

	var events []daemon.ChatEvent
	err := h.svc.ChatStream(context.Background(), h.alice, "",
		[]daemon.ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		func(ev daemon.ChatEvent) error {
			events = append(events, ev)
			return nil
		})
	if err != nil {
		t.Fatalf("ChatStream returned err = %v; store-off should surface via event", err)
	}
	if len(events) != 1 || events[0].Type != "error" {
		t.Fatalf("events = %+v, want a single error event", events)
	}
	if events[0].ErrorCode != "encrypted_store_off" {
		t.Errorf("error code = %q, want encrypted_store_off", events[0].ErrorCode)
	}
}

// newTestEncryptedSecrets builds an in-memory tenant secret store
// for chat tests. Uses a zero master key — bytes are arbitrary as
// long as the length is right; the test is exercising the lookup
// path, not the crypto.
func newTestEncryptedSecrets(t *testing.T) *daemon.EncryptedSecrets {
	t.Helper()
	masterKey := make([]byte, 32)
	es, err := daemon.NewEncryptedSecrets(masterKey, daemon.NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	return es
}

// jsonString quotes s as a JSON string — needed in the SSE script
// where we embed the proposal body as a string literal inside
// input_json_delta.partial_json.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
