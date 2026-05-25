package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// isClaudeCLIMode reports whether hzd was started with -claude-cli
// (cmd/hzd/main.go publishes the toggle as $HAZYFLOW_CLAUDE_CLI for
// the integrations package to read; the daemon-side check lives on
// Service.UseClaudeCLI).
func isClaudeCLIMode() bool {
	return os.Getenv("HAZYFLOW_CLAUDE_CLI") == "1"
}

// claudeCLIBinary is the path/name of the local Claude Code CLI. A
// package-level var so tests can swap it for a stub without
// monkey-patching exec.LookPath.
var claudeCLIBinary = "claude"

// runClaudeCLI is the actual subprocess invocation. Tests substitute
// a stub here to verify routing without depending on the real CLI.
var runClaudeCLI = invokeClaudeCLI

// executeClaudeViaCLI is the alternative path the Claude drop takes
// when hzd is in claude-cli mode. Instead of POSTing to Anthropic's
// Messages API, it shells out to the locally-installed `claude -p`,
// which authenticates against the user's existing OAuth / keychain
// login. No API key required.
//
// The contract matches executeClaude as closely as practical:
//   - Same output port names ("text", "response").
//   - Same "prompt input → params.messages → params.prompt"
//     precedence, fed through the same coercePromptText helper.
//   - Errors carry the same code shape (bad_param, cli_failed, …).
//
// Caveats vs the API path:
//   - max_tokens / temperature / stop_sequences are ignored — the
//     CLI doesn't expose them. Documented in the manifest.
//   - The "response" output is a small synthetic envelope rather
//     than the full Anthropic Messages response, since the CLI
//     emits different metadata (session_id, num_turns, etc.).
func executeClaudeViaCLI(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	req, err := buildClaudeRequest(job)
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	if len(req.Messages) == 0 {
		return errResult(job, "bad_input",
			"no messages — provide params.messages or the prompt input port"), nil
	}

	// Flatten the messages into a single transcript prompt. claude -p
	// is single-shot; multi-turn would require --resume + session IDs
	// which is overkill for what this drop does.
	prompt := flattenForCLI(req.Messages)

	args := []string{
		"-p",
		"--output-format", "json", // single JSON object on stdout
		// Block claude's local tools — this is "talk to the model",
		// not "let claude touch the filesystem".
		"--disallowedTools",
		"Bash Edit Read Write Glob Grep WebFetch WebSearch Skill",
		"--dangerously-skip-permissions",
	}
	if req.System != "" {
		args = append(args, "--append-system-prompt", req.System)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, prompt)

	stdout, err := runClaudeCLI(ctx, args)
	if err != nil {
		return errResult(job, "cli_failed", err.Error()), nil
	}

	// claude -p --output-format json emits a single envelope like
	//   {"type":"result","subtype":"success","is_error":false,
	//    "result":"the assistant text","session_id":"…", …}
	var env struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
		Model   string `json:"model"`
		Usage   any    `json:"usage"`
		Session string `json:"session_id"`
	}
	if jerr := json.Unmarshal(stdout, &env); jerr != nil {
		// Fall back to raw text — better than failing the node when
		// the CLI prints something unexpected.
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: map[string]core.Ref{
				"text":     {MIME: "text/plain", Inline: strings.TrimSpace(string(stdout))},
				"response": {MIME: "application/json", Inline: map[string]any{"raw_stdout": string(stdout)}},
			},
		}, nil
	}
	if env.IsError {
		return errResult(job, "cli_error", env.Result), nil
	}
	respMap := map[string]any{
		"result":     env.Result,
		"model":      env.Model,
		"usage":      env.Usage,
		"session_id": env.Session,
		"transport":  "claude-cli",
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"text":     {MIME: "text/plain", Inline: env.Result},
			"response": {MIME: "application/json", Inline: respMap},
		},
	}, nil
}

// flattenForCLI turns the conversation history into a single prompt
// string. Same shape the daemon's chat agent uses to talk to
// claude -p (daemon/claudecli.go) — "ROLE: content" lines separated
// by blank lines, ending with "ASSISTANT:" so the model knows where
// to start.
func flattenForCLI(msgs []claudeMessage) string {
	if len(msgs) == 1 && msgs[0].Role == "user" {
		// Single user turn: don't bother with the transcript
		// framing, just send the content. Common case (the prompt
		// input port path) and reads cleaner in the CLI logs.
		return msgs[0].Content
	}
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(strings.ToUpper(m.Role))
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n\n")
	}
	b.WriteString("ASSISTANT: ")
	return b.String()
}

// invokeClaudeCLI is the default runClaudeCLI implementation. Calls
// `claude` with the supplied args and returns stdout. Stderr is
// included in the error message on non-zero exit so the user sees
// auth failures and the like.
func invokeClaudeCLI(ctx context.Context, args []string) ([]byte, error) {
	if _, err := exec.LookPath(claudeCLIBinary); err != nil {
		return nil, fmt.Errorf("claude CLI not on PATH (HAZYFLOW_CLAUDE_CLI is set but the binary is missing): %w", err)
	}
	cmd := exec.CommandContext(ctx, claudeCLIBinary, args...)
	stdout, err := cmd.Output()
	if err != nil {
		// exec.ExitError carries Stderr — surface it so a 401 or
		// "not logged in" from claude reaches the user.
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("claude -p: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("claude -p: %w", err)
	}
	return stdout, nil
}
