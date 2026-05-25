package ai

import (
	"context"
	"os"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// stubClaudeCLI swaps runClaudeCLI for the duration of the test
// and captures the args it was called with. Returns a func that
// returns the stubbed args so assertions can inspect them.
func stubClaudeCLI(t *testing.T, response string) *[]string {
	t.Helper()
	prev := runClaudeCLI
	var capturedArgs []string
	runClaudeCLI = func(_ context.Context, args []string) ([]byte, error) {
		capturedArgs = args
		return []byte(response), nil
	}
	t.Cleanup(func() { runClaudeCLI = prev })
	return &capturedArgs
}

// When HAZYFLOW_CLAUDE_CLI=1 the drop must skip the api_key check
// and invoke `claude -p` instead. The api_key is intentionally
// absent — failing here would prove the env-gated routing isn't
// firing, which is exactly the bug the user reported.
func TestClaude_CLIMode_RoutesToSubprocess(t *testing.T) {
	t.Setenv("HAZYFLOW_CLAUDE_CLI", "1")
	args := stubClaudeCLI(t, `{"type":"result","subtype":"success","is_error":false,"result":"hello back","model":"claude-sonnet-4-6","session_id":"s_1"}`)

	res, err := executeClaude(context.Background(), core.Job{
		// NOTE: no api_key — CLI mode bypasses that requirement.
		Params: map[string]any{"prompt": "say hi"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q error=%+v", res.Status, res.Error)
	}
	if text, _ := res.Output["text"].Inline.(string); text != "hello back" {
		t.Errorf("text = %q, want hello back", text)
	}
	// The stub should have been invoked with -p and the prompt
	// trailing. Spot-check both ends so a refactor that drops one
	// of them gets caught.
	if len(*args) == 0 || (*args)[0] != "-p" {
		t.Errorf("args = %v, want -p first", *args)
	}
	if last := (*args)[len(*args)-1]; last != "say hi" {
		t.Errorf("last arg = %q, want \"say hi\"", last)
	}
}

// Env unset → original API path, which (without an api_key) errors
// with the same friendly bad_param it always did. Belt-and-braces
// that we haven't regressed the api-key flow for users who DON'T
// have claude-cli mode on.
func TestClaude_CLIMode_OffStillRequiresAPIKey(t *testing.T) {
	_ = os.Unsetenv("HAZYFLOW_CLAUDE_CLI")
	res, _ := executeClaude(context.Background(), core.Job{
		Params: map[string]any{"prompt": "hi"},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Errorf("status=%q error=%+v, want bad_param (api_key)", res.Status, res.Error)
	}
}

// System prompt forwards to --append-system-prompt. Catches a
// likely future refactor that "simplifies" the args builder.
func TestClaude_CLIMode_ForwardsSystemPrompt(t *testing.T) {
	t.Setenv("HAZYFLOW_CLAUDE_CLI", "1")
	args := stubClaudeCLI(t, `{"type":"result","subtype":"success","is_error":false,"result":"ok"}`)

	_, _ = executeClaude(context.Background(), core.Job{
		Params: map[string]any{
			"prompt": "hi",
			"system": "be terse",
		},
	}, nil)
	joined := strings.Join(*args, " ")
	if !strings.Contains(joined, "--append-system-prompt be terse") {
		t.Errorf("args = %v (missing system prompt forwarding)", *args)
	}
}

// Merge → Claude wiring exercise: list-shaped prompt input must
// flow through coercePromptText into the CLI prompt verbatim.
// Locks in that the CLI path uses buildClaudeRequest (and therefore
// the coercion) instead of a separate prompt-resolution path.
func TestClaude_CLIMode_AcceptsMergeListPrompt(t *testing.T) {
	t.Setenv("HAZYFLOW_CLAUDE_CLI", "1")
	args := stubClaudeCLI(t, `{"type":"result","subtype":"success","is_error":false,"result":"x"}`)

	_, _ = executeClaude(context.Background(), core.Job{
		Params: map[string]any{},
		Input: map[string]core.Ref{
			"prompt": {
				MIME: "application/x-hazyflow-list+json",
				Inline: []core.Ref{
					{Inline: "alpha"},
					{Inline: "beta"},
				},
			},
		},
	}, nil)
	last := (*args)[len(*args)-1]
	if last != "alpha\n\nbeta" {
		t.Errorf("CLI prompt = %q, want \"alpha\\n\\nbeta\"", last)
	}
}

// is_error=true in the CLI envelope must surface as a node failure
// with code cli_error. Silently treating it as success would let a
// "Not logged in" message land in the assistant text and corrupt
// downstream nodes.
func TestClaude_CLIMode_CLIErrorBecomesNodeFailure(t *testing.T) {
	t.Setenv("HAZYFLOW_CLAUDE_CLI", "1")
	stubClaudeCLI(t, `{"type":"result","subtype":"success","is_error":true,"result":"Not logged in"}`)

	res, _ := executeClaude(context.Background(), core.Job{
		Params: map[string]any{"prompt": "hi"},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "cli_error" {
		t.Errorf("status=%q error=%+v, want cli_error", res.Status, res.Error)
	}
	if !strings.Contains(res.Error.Message, "Not logged in") {
		t.Errorf("error message = %q, want it to carry the CLI text", res.Error.Message)
	}
}
