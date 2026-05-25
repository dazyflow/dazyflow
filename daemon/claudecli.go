package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// streamViaClaudeCLI is the test-mode chat backend. Instead of calling
// the Anthropic API directly, it shells out to `claude -p` (the
// Claude Code CLI) and points it at our MCP server, so the user's
// already-installed claude session powers the chat — useful for
// developing the chat UI / flow surface without burning API credits.
//
// Architecture per chat request:
//
//	browser → POST /chat/stream (hzd)
//	            └─ Service.ChatStream
//	                └─ streamViaClaudeCLI
//	                    └─ exec claude -p --mcp-config <tmp>
//	                          ├─ stream-json on stdout
//	                          └─ MCP subprocess: hz-mcp
//	                                └─ HTTP /api/v1 (hzd, this process)
//
// Multi-turn history is folded into the prompt as a transcript —
// each chat invocation spawns a fresh `claude -p` (single-shot from
// claude's POV). The user's conversation context survives because
// we always send the full prior transcript. That's coarser than the
// API path but cheap and predictable.
func (s *Service) streamViaClaudeCLI(
	ctx context.Context,
	p core.Principal,
	bearerToken string,
	msgs []ChatMessage,
	onEvent func(ChatEvent) error,
) error {
	if s.ClaudeCLIMCPBinary == "" {
		return fmt.Errorf("claude-cli mode: ClaudeCLIMCPBinary must be set (path to hz-mcp)")
	}
	if bearerToken == "" {
		return fmt.Errorf("claude-cli mode: no bearer token available; hz-mcp needs one to call back into hzd")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return fmt.Errorf("claude-cli mode: claude binary not on PATH: %w", err)
	}

	mcpConfig, cleanup, err := s.writeMCPConfig(bearerToken)
	if err != nil {
		return fmt.Errorf("write mcp config: %w", err)
	}
	defer cleanup()

	prompt := buildClaudeCLIPrompt(msgs)

	// White-list only our MCP tools so claude doesn't go reading
	// files or running Bash on the developer's machine while
	// servicing an end-user chat. The Skill tool stays off too
	// because it can transitively invoke other things.
	allowedTools := strings.Join([]string{
		"mcp__hazy-flow__list_drops",
		"mcp__hazy-flow__list_flows",
		"mcp__hazy-flow__get_flow",
		"mcp__hazy-flow__create_flow",
		"mcp__hazy-flow__update_flow",
		"mcp__hazy-flow__run_flow",
		"mcp__hazy-flow__get_run",
		"mcp__hazy-flow__list_runs",
		"mcp__hazy-flow__wait_for_run",
		"mcp__hazy-flow__cancel_run",
		"mcp__hazy-flow__list_pending_approvals",
		"mcp__hazy-flow__approve_node",
	}, " ")

	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose", // required by --output-format=stream-json
		"--mcp-config", mcpConfig,
		"--allowedTools", allowedTools,
		// Disallow everything not on the allow-list — the docs say
		// --allowedTools alone limits the *picker* but isn't
		// authoritative, so we belt-and-braces.
		"--disallowedTools", "Bash Edit Read Write Glob Grep WebFetch WebSearch Skill",
		// Skip interactive permission prompts — this is non-
		// interactive by definition.
		"--dangerously-skip-permissions",
		// Intentionally NOT --bare: --bare forces ANTHROPIC_API_KEY
		// as the only auth path and ignores the keychain. We want
		// the user's existing `claude /login` (OAuth + Claude Code
		// subscription) to work without extra env setup.
		"--append-system-prompt", chatSystemPrompt,
		prompt,
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	// Inherit hzd's env so claude can reach the user's keychain /
	// OAuth login (gnome-keyring, libsecret, etc. all key off env).
	// Do NOT set CLAUDE_CODE_SIMPLE=1 — that's the env-form of
	// --bare and disables OAuth-only auth, forcing ANTHROPIC_API_KEY.
	cmd.Env = os.Environ()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}
	// Drain stderr to our logger so claude's diagnostic output
	// surfaces somewhere visible without polluting the SSE stream.
	go drainStderr(stderr)

	if err := parseClaudeCLIStream(stdout, onEvent); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	if err := cmd.Wait(); err != nil {
		// Non-zero exit after we've emitted events is reported but
		// doesn't override what the user already saw — the events
		// are the source of truth.
		log.Printf("claude-cli: %v", err)
	}
	return nil
}

// buildClaudeCLIPrompt flattens the conversation into a single
// prompt string. claude -p is stateless per invocation, so we
// re-send the whole transcript every turn.
//
// The system prompt itself rides on --append-system-prompt; the
// transcript here is "ROLE: text" newline-separated. claude is good
// at picking up on this shape — better than wrapping it in
// pseudo-JSON we'd have to invent.
func buildClaudeCLIPrompt(msgs []ChatMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		role := m.Role
		var text string
		// Content might be either a JSON string or a content-block
		// array. For the CLI path we only care about the plain user
		// messages — anything fancier (mid-tool turns from a prior
		// API session) is dropped.
		var s string
		if err := json.Unmarshal(m.Content, &s); err == nil {
			text = s
		} else {
			text = string(m.Content)
		}
		b.WriteString(strings.ToUpper(role))
		b.WriteString(": ")
		b.WriteString(text)
		b.WriteString("\n\n")
	}
	b.WriteString("ASSISTANT: ")
	return b.String()
}

// writeMCPConfig builds a one-shot MCP config that points claude at
// our hz-mcp binary, with the env it needs to call back into hzd.
// Returns the temp path and a cleanup func.
func (s *Service) writeMCPConfig(bearerToken string) (string, func(), error) {
	hzdURL := s.ClaudeCLIHazydURL
	if hzdURL == "" {
		// Best-effort default. Operators on non-loopback setups
		// must set this explicitly via flag.
		hzdURL = "http://localhost:8080"
	}
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"hazy-flow": map[string]any{
				"command": s.ClaudeCLIMCPBinary,
				"env": map[string]string{
					"HAZYFLOW_URL":     hzdURL,
					"HAZYFLOW_API_KEY": bearerToken,
				},
			},
		},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp("", "hazy-mcp-*.json")
	if err != nil {
		return "", nil, err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }
	return path, cleanup, nil
}

// parseClaudeCLIStream reads NDJSON from claude -p's stdout and
// emits ChatEvents to the consumer. We deliberately don't
// re-implement the entire Anthropic-content-block protocol — only
// the slice the chat UI needs:
//
//	type=assistant + content.text → "text"
//	type=assistant + content.tool_use → "tool_use_start"
//	type=user + content.tool_result → "tool_use_result"
//	type=result → "done"
//	type=system / rate_limit_event / etc → ignored
func parseClaudeCLIStream(r io.Reader, onEvent func(ChatEvent) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			// Garbage line — log via stderr eventually; don't
			// kill the stream.
			continue
		}
		var t string
		_ = json.Unmarshal(raw["type"], &t)
		switch t {
		case "assistant":
			if err := dispatchAssistant(raw["message"], onEvent); err != nil {
				return err
			}
		case "user":
			if err := dispatchUserToolResult(raw["message"], onEvent); err != nil {
				return err
			}
		case "result":
			var res struct {
				StopReason string `json:"stop_reason"`
				IsError    bool   `json:"is_error"`
				Result     string `json:"result"`
			}
			_ = json.Unmarshal(line, &res)
			ev := ChatEvent{Type: "done", StopReason: res.StopReason}
			if res.IsError {
				ev = ChatEvent{Type: "error", ErrorText: res.Result}
			}
			if err := onEvent(ev); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}

// dispatchAssistant walks an assistant message's content blocks and
// emits one ChatEvent per useful block. Thinking blocks are dropped
// (they're noise in the chat UI); text becomes streaming "text"
// events; tool_use blocks become "tool_use_start" plus, when the
// invoked tool is mcp__hazy-flow__create_flow / update_flow, an
// extra "proposal" event with the input graph so the UI can render
// an "applied" card.
func dispatchAssistant(messageRaw json.RawMessage, onEvent func(ChatEvent) error) error {
	var m struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(messageRaw, &m); err != nil {
		return nil
	}
	for _, c := range m.Content {
		var cb struct {
			Type  string          `json:"type"`
			Text  string          `json:"text,omitempty"`
			ID    string          `json:"id,omitempty"`
			Name  string          `json:"name,omitempty"`
			Input json.RawMessage `json:"input,omitempty"`
		}
		if err := json.Unmarshal(c, &cb); err != nil {
			continue
		}
		switch cb.Type {
		case "text":
			if cb.Text != "" {
				if err := onEvent(ChatEvent{Type: "text", Delta: cb.Text}); err != nil {
					return err
				}
			}
		case "tool_use":
			if err := onEvent(ChatEvent{
				Type:      "tool_use_start",
				ToolID:    cb.ID,
				Tool:      cb.Name,
				ToolInput: cb.Input,
			}); err != nil {
				return err
			}
			// Surface create_flow / update_flow as a proposal event
			// so the chat panel can render the change. In this code
			// path the MCP tool call IS the save, so we flag the
			// event auto-applied — the UI then renders a "saved"
			// state without an Apply button (clicking Apply on
			// identical content would otherwise round-trip through
			// the workspace store as a no-op success at best, or a
			// confusing error on older daemons).
			if cb.Name == "mcp__hazy-flow__create_flow" || cb.Name == "mcp__hazy-flow__update_flow" {
				var g core.Graph
				if err := json.Unmarshal(cb.Input, &g); err == nil && g.ID != "" {
					_ = onEvent(ChatEvent{Type: "proposal", Proposal: &g, AutoApplied: true})
				}
			}
		}
	}
	return nil
}

// dispatchUserToolResult turns claude's wrapped tool-result frames
// (which it represents as user-role messages) into our
// tool_use_result events. Truncates large payloads so the UI
// doesn't choke on a 100KB tool dump.
func dispatchUserToolResult(messageRaw json.RawMessage, onEvent func(ChatEvent) error) error {
	var m struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(messageRaw, &m); err != nil {
		return nil
	}
	for _, c := range m.Content {
		var tr struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
			Content   any    `json:"content"`
			IsError   bool   `json:"is_error"`
		}
		if err := json.Unmarshal(c, &tr); err != nil || tr.Type != "tool_result" {
			continue
		}
		text := stringifyToolResult(tr.Content)
		ev := ChatEvent{
			Type:       "tool_use_result",
			ToolID:     tr.ToolUseID,
			ResultText: truncateForUI(text, 1024),
		}
		if tr.IsError {
			ev.ErrorText = ev.ResultText
			ev.ResultText = ""
		}
		if err := onEvent(ev); err != nil {
			return err
		}
	}
	return nil
}

// stringifyToolResult flattens whatever shape claude gave us into a
// preview string. The MCP-tool result is usually a string already;
// sometimes it arrives as `[{type: "text", text: "..."}]`.
func stringifyToolResult(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var parts []string
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				if t, _ := m["text"].(string); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func truncateForUI(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func drainStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		log.Printf("claude-cli stderr: %s", scanner.Text())
	}
}

// ResolveClaudeMCPBinary picks the hz-mcp binary path used when
// claude-cli mode is on. Honors an explicit override (flag) first,
// then $HZ_MCP_BIN, then PATH lookup. Returns "" if not found —
// the streamViaClaudeCLI guard surfaces a clear error.
func ResolveClaudeMCPBinary(explicit string) string {
	if explicit != "" {
		if abs, err := filepath.Abs(explicit); err == nil {
			return abs
		}
		return explicit
	}
	if env := os.Getenv("HZ_MCP_BIN"); env != "" {
		return env
	}
	if p, err := exec.LookPath("hz-mcp"); err == nil {
		return p
	}
	return ""
}
