package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon/anthropic"
)

// ChatEvent is the protocol between Service.ChatStream and its
// consumer (the HTTP SSE handler in the gateway). One event per
// significant transition: a token delta, a tool dispatch, a
// proposal ready for the user, the agent ending its turn.
type ChatEvent struct {
	// Type names the event variant. Consumers switch on it. Values:
	//   "text"             — incremental assistant text (Delta set)
	//   "tool_use_start"   — agent invoked Tool/ID with Input
	//   "tool_use_result"  — tool returned ResultText (or ErrorText)
	//   "proposal"         — agent proposes the Graph; user confirms
	//   "done"             — final stop_reason for the assistant turn
	//   "error"            — fatal error; the chat must stop
	Type string `json:"type"`

	Delta string `json:"delta,omitempty"`

	ToolID    string          `json:"tool_id,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`

	ResultText string `json:"result_text,omitempty"`
	ErrorText  string `json:"error_text,omitempty"`

	Proposal *core.Graph `json:"proposal,omitempty"`

	// AutoApplied is true when the agent already wrote the proposed
	// graph through its tools (claude-cli + MCP mode does this — the
	// model's tool call is the save). The UI suppresses the Apply
	// button and just shows the result so the user doesn't get a
	// confusing no-op error when clicking Apply on already-saved
	// content.
	AutoApplied bool `json:"auto_applied,omitempty"`

	StopReason string `json:"stop_reason,omitempty"`
}

// ChatMessage is a user-facing message in the agent's conversation.
// Role is "user" or "assistant". Content is plain text for inbound
// user messages; assistant messages built mid-loop carry their full
// content-block array (text + tool_use) — that's why Content is a
// json.RawMessage end-to-end.
type ChatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

const (
	chatMaxTurns   = 10
	chatMaxTokens  = 4096
	chatTotalBudget = 5 * time.Minute
)

// chatSystemPrompt is the agent's standing instruction. We keep it
// terse — list_drops returns full manifests, so the model can
// discover modules without us pasting them into the system prompt.
const chatSystemPrompt = `You are a Hazy Flow authoring assistant. The user
wants to build or modify a pipeline. You can READ the workspace (list_drops,
list_flows, get_flow) and PROPOSE a graph (propose_flow). You DO NOT directly
overwrite the user's flow — propose_flow returns the proposal to the user, who
clicks "Apply" to commit it.

When the user describes a goal:
  1. If you don't know what modules are available, call list_drops first.
  2. If they reference an existing flow ("update my onboarding flow"), call
     get_flow to see the current state.
  3. Construct a valid Hazy Flow graph: nodes have id + module + params;
     edges have from / from_port / to / to_port. Use sensible IDs
     ("trigger", "filter_records", etc.) — they appear in run logs.
  4. Call propose_flow with the FULL graph. After it returns, finish your
     turn with a brief summary of what you propose; do not call propose_flow
     more than once unless the user iterates.

Always explain WHY you chose specific modules so the user can correct your
assumptions before clicking Apply.`

// ChatStream runs the agent loop until stop_reason=end_turn, the
// turn budget runs out, or onEvent returns an error. Routes to one
// of two backends:
//
//   - claude-cli mode (UseClaudeCLI=true): shells out to the local
//     `claude -p` with our MCP server attached. bearerToken is
//     forwarded to hz-mcp so it can call back into hzd.
//   - api mode (default): calls Anthropic's Messages API directly
//     with AnthropicAPIKey. bearerToken is unused.
func (s *Service) ChatStream(
	ctx context.Context,
	p core.Principal,
	bearerToken string,
	userMessages []ChatMessage,
	onEvent func(ChatEvent) error,
) error {
	if len(userMessages) == 0 {
		return fmt.Errorf("at least one user message required")
	}
	if s.UseClaudeCLI {
		return s.streamViaClaudeCLI(ctx, p, bearerToken, userMessages, onEvent)
	}
	if s.AnthropicAPIKey == "" {
		return fmt.Errorf("anthropic API key not configured")
	}

	client := anthropic.NewClient(s.AnthropicAPIKey)
	if s.AnthropicBaseURL != "" {
		client.BaseURL = s.AnthropicBaseURL
	}

	// Convert chat messages to anthropic.Message. We pass content
	// through verbatim — the caller is responsible for sending
	// either a string ("hello") or a content-block array.
	msgs := make([]anthropic.Message, len(userMessages))
	for i, m := range userMessages {
		msgs[i] = anthropic.Message{Role: m.Role, Content: m.Content}
	}

	tools := chatToolDefinitions()
	deadline := time.Now().Add(chatTotalBudget)

	for turn := 0; turn < chatMaxTurns; turn++ {
		if time.Now().After(deadline) {
			return onEvent(ChatEvent{Type: "error", ErrorText: "chat exceeded time budget"})
		}
		// One assistant turn: collect text + tool_use blocks as they
		// stream in, then act on tool_uses at the end of the turn.
		blocks, stopReason, err := s.streamAssistantTurn(ctx, client, msgs, tools, onEvent)
		if err != nil {
			_ = onEvent(ChatEvent{Type: "error", ErrorText: err.Error()})
			return err
		}

		// Append the assistant turn verbatim to the conversation
		// state so the next call sees what the model said.
		assistantContent, _ := json.Marshal(blocks)
		msgs = append(msgs, anthropic.Message{Role: "assistant", Content: assistantContent})

		// Did the model ask to call tools?
		toolUses := collectToolUses(blocks)
		if len(toolUses) == 0 {
			// No tools requested — the turn is done regardless of
			// stop_reason (end_turn, max_tokens, etc.).
			return onEvent(ChatEvent{Type: "done", StopReason: stopReason})
		}

		// Execute each tool_use sequentially. Building a single
		// tool_result message back to the model.
		resultBlocks := make([]map[string]any, 0, len(toolUses))
		for _, tu := range toolUses {
			if err := onEvent(ChatEvent{
				Type:      "tool_use_start",
				ToolID:    tu.ID,
				Tool:      tu.Name,
				ToolInput: tu.Input,
			}); err != nil {
				return err
			}
			resultText, isErr, proposal := s.dispatchChatTool(ctx, p, tu.Name, tu.Input)
			if proposal != nil {
				if err := onEvent(ChatEvent{Type: "proposal", Proposal: proposal}); err != nil {
					return err
				}
			}
			ev := ChatEvent{
				Type:       "tool_use_result",
				ToolID:     tu.ID,
				ResultText: resultText,
			}
			if isErr {
				ev.ErrorText = resultText
				ev.ResultText = ""
			}
			if err := onEvent(ev); err != nil {
				return err
			}
			block := map[string]any{
				"type":        "tool_result",
				"tool_use_id": tu.ID,
				"content":     resultText,
			}
			if isErr {
				block["is_error"] = true
			}
			resultBlocks = append(resultBlocks, block)
		}
		toolResultContent, _ := json.Marshal(resultBlocks)
		msgs = append(msgs, anthropic.Message{Role: "user", Content: toolResultContent})
	}
	return onEvent(ChatEvent{Type: "error", ErrorText: fmt.Sprintf("agent exceeded max turns (%d)", chatMaxTurns)})
}

// streamAssistantTurn handles one round-trip to Anthropic, returning
// the full assembled content-block list and the stop_reason. Text
// deltas are forwarded to onEvent as they arrive; tool_use blocks
// are buffered and surfaced only at the end of the turn (so we can
// dispatch them as a coherent batch).
func (s *Service) streamAssistantTurn(
	ctx context.Context,
	client *anthropic.Client,
	msgs []anthropic.Message,
	tools []anthropic.Tool,
	onEvent func(ChatEvent) error,
) ([]map[string]any, string, error) {
	// blockAccum[i] holds the in-progress block at index i. text
	// content uses .text; tool_use uses .id/.name/.input (accumulated
	// from input_json_delta partials).
	type accum struct {
		Type     string
		Text     strings.Builder
		ID       string
		Name     string
		RawInput strings.Builder
	}
	blocks := map[int]*accum{}
	var stopReason string

	err := client.Stream(ctx, anthropic.Request{
		Model:     s.AnthropicModel(),
		System:    chatSystemPrompt,
		Messages:  msgs,
		MaxTokens: chatMaxTokens,
		Tools:     tools,
	}, func(ev anthropic.Event) error {
		switch ev.Type {
		case "content_block_start":
			if ev.Block == nil {
				return nil
			}
			a := &accum{Type: ev.Block.Type, ID: ev.Block.ID, Name: ev.Block.Name}
			blocks[ev.Index] = a
		case "content_block_delta":
			a, ok := blocks[ev.Index]
			if !ok {
				return nil
			}
			if ev.TextDelta != "" {
				a.Text.WriteString(ev.TextDelta)
				return onEvent(ChatEvent{Type: "text", Delta: ev.TextDelta})
			}
			if ev.PartialJSON != "" {
				a.RawInput.WriteString(ev.PartialJSON)
			}
		case "message_delta":
			stopReason = ev.StopReason
		case "error":
			if ev.Error != nil {
				return ev.Error
			}
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}

	// Materialize the buffered blocks in their original index order
	// — the wire shape is a top-level array, so order matters when
	// we ship the assistant message back next turn.
	out := make([]map[string]any, 0, len(blocks))
	for i := 0; i < len(blocks); i++ {
		a, ok := blocks[i]
		if !ok {
			continue
		}
		switch a.Type {
		case "text":
			out = append(out, map[string]any{
				"type": "text",
				"text": a.Text.String(),
			})
		case "tool_use":
			// Empty input is "{}" — never "" — because Anthropic
			// rejects tool_use blocks that lack an input object.
			raw := a.RawInput.String()
			if raw == "" {
				raw = "{}"
			}
			var parsed any
			if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
				// Surface the partial we got — the next turn's
				// tool_result will return an error and the model
				// can recover.
				parsed = map[string]any{}
			}
			out = append(out, map[string]any{
				"type":  "tool_use",
				"id":    a.ID,
				"name":  a.Name,
				"input": parsed,
			})
		}
	}
	return out, stopReason, nil
}

type toolUseRef struct {
	ID    string
	Name  string
	Input json.RawMessage
}

func collectToolUses(blocks []map[string]any) []toolUseRef {
	var out []toolUseRef
	for _, b := range blocks {
		if t, _ := b["type"].(string); t != "tool_use" {
			continue
		}
		input, _ := json.Marshal(b["input"])
		out = append(out, toolUseRef{
			ID:    asString(b["id"]),
			Name:  asString(b["name"]),
			Input: input,
		})
	}
	return out
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// dispatchChatTool runs one tool by name and returns (text, isError,
// proposal). The text payload is what flows back to the model as
// tool_result.content; proposal — when non-nil — fires a separate
// "proposal" event to the UI without entering the model's context.
func (s *Service) dispatchChatTool(ctx context.Context, p core.Principal, name string, input json.RawMessage) (string, bool, *core.Graph) {
	switch name {
	case "list_drops":
		return s.chatToolListDrops(ctx, p)
	case "list_flows":
		return s.chatToolListFlows(ctx, p, input)
	case "get_flow":
		return s.chatToolGetFlow(ctx, p, input)
	case "propose_flow":
		return s.chatToolProposeFlow(ctx, p, input)
	}
	return fmt.Sprintf("unknown tool: %s", name), true, nil
}

// chatToolDefinitions returns the JSON-Schema-bound tool list we
// hand to Anthropic. Schemas mirror what the MCP server already
// publishes for the same operations.
func chatToolDefinitions() []anthropic.Tool {
	return []anthropic.Tool{
		{
			Name:        "list_drops",
			Description: "List every flow node (module) the daemon knows about. Returns the full manifest (label, description, inputs/outputs, params_schema) for each. Call BEFORE propose_flow if you don't already know what modules exist.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			Name:        "list_flows",
			Description: "List flow IDs in the current workspace. Use to check whether a flow already exists before proposing.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			Name:        "get_flow",
			Description: "Fetch an existing flow's full graph (nodes, edges, settings) so you can propose changes off it.",
			InputSchema: json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"string"}}}`),
		},
		{
			Name: "propose_flow",
			// Propose-mode is critical: the docstring tells the
			// model NOT to expect a save, just user review.
			Description: "Propose a graph for the user to review. The user clicks Apply in the UI to commit it; this tool does NOT save the flow. Pass the COMPLETE graph (id, nodes, edges, optional triggers/visibility/name/icon/description/timeout_seconds). Returns a short acknowledgement; the actual proposal payload is surfaced to the UI out of band.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"required":["id","nodes"],
				"properties":{
					"id":              {"type":"string"},
					"name":            {"type":"string"},
					"description":     {"type":"string"},
					"icon":            {"type":"string"},
					"visibility":      {"type":"string","enum":["org","private"]},
					"timeout_seconds": {"type":"integer","minimum":0},
					"nodes": {
						"type":"array",
						"items": {
							"type":"object",
							"required":["id","module"],
							"properties":{
								"id":     {"type":"string"},
								"module": {"type":"string"},
								"params": {"type":"object"},
								"timeout_seconds": {"type":"integer","minimum":0}
							}
						}
					},
					"edges": {
						"type":"array",
						"items": {
							"type":"object",
							"required":["from","to"],
							"properties":{
								"from":      {"type":"string"},
								"from_port": {"type":"string","default":"out"},
								"to":        {"type":"string"},
								"to_port":   {"type":"string","default":"in"},
								"on_error":  {"type":"string","enum":["","abort","skip","retry","fallback"]}
							}
						}
					}
				}
			}`),
		},
	}
}

// ─────────────────────────── tool handlers ───────────────────────────

func (s *Service) chatToolListDrops(ctx context.Context, p core.Principal) (string, bool, *core.Graph) {
	drops, err := s.ListDrops(ctx, p)
	if err != nil {
		return err.Error(), true, nil
	}
	b, _ := json.MarshalIndent(drops, "", "  ")
	return string(b), false, nil
}

func (s *Service) chatToolListFlows(ctx context.Context, p core.Principal, _ json.RawMessage) (string, bool, *core.Graph) {
	ids, err := s.ListGraphs(ctx, p, p.Tenant, p.Workspace)
	if err != nil {
		return err.Error(), true, nil
	}
	b, _ := json.MarshalIndent(map[string]any{"flows": ids}, "", "  ")
	return string(b), false, nil
}

func (s *Service) chatToolGetFlow(ctx context.Context, p core.Principal, input json.RawMessage) (string, bool, *core.Graph) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return fmt.Sprintf("decode args: %v", err), true, nil
	}
	if args.ID == "" {
		return "id is required", true, nil
	}
	g, err := s.LoadGraph(ctx, p, p.Tenant, p.Workspace, args.ID, "")
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return fmt.Sprintf("flow %q not found", args.ID), true, nil
		}
		return err.Error(), true, nil
	}
	b, _ := json.MarshalIndent(g, "", "  ")
	return string(b), false, nil
}

// chatToolProposeFlow does NOT write — it builds a core.Graph from
// the agent's args, runs validation, and surfaces it as a proposal
// event the user reviews and Apply-clicks. The text returned to the
// model is short and confirms the proposal landed; the UI is the
// real consumer.
func (s *Service) chatToolProposeFlow(_ context.Context, p core.Principal, input json.RawMessage) (string, bool, *core.Graph) {
	var g core.Graph
	if err := json.Unmarshal(input, &g); err != nil {
		return fmt.Sprintf("decode graph: %v", err), true, nil
	}
	// Force scope onto the principal's tenant/workspace so the agent
	// can't propose flows in a tenant it isn't authenticated against.
	g.Tenant = p.Tenant
	g.Workspace = p.Workspace
	if err := core.Validate(g); err != nil {
		return fmt.Sprintf("proposal invalid: %v", err), true, nil
	}
	// Tell the model the proposal is queued. Brief on purpose:
	// repeating the entire graph back to the model wastes tokens
	// and tempts it to re-propose.
	return fmt.Sprintf("Proposal for flow %q queued for user review. Wait for the user to apply or revise before proposing again.", g.ID), false, &g
}

// AnthropicModel returns the model name the chat agent uses,
// honoring s.AnthropicModelOverride for tests / per-deploy tuning.
func (s *Service) AnthropicModel() string {
	if s.AnthropicModelOverride != "" {
		return s.AnthropicModelOverride
	}
	return anthropic.DefaultModel
}
