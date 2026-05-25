package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

// slackPostURL is the chat.postMessage endpoint. Declared as a var so
// tests can swap it for an httptest server without monkey-patching the
// http client.
var slackPostURL = "https://slack.com/api/chat.postMessage"

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "slack_post",
			Version:        "1.0",
			Label:          "Slack post",
			Color:          "#4a154b",
			Icon:           "message-square",
			Category:       "notification",
			Provider:       "slack",
			Integration:    "Slack",
			Tags:           []string{"slack", "chat", "notify", "message"},
			Description:    "Post a message to a Slack channel via chat.postMessage. The bot token MUST come through ${secret:NAME} — embedding xoxb tokens in the graph payload is a leak waiting to happen. For incoming-webhook URLs use webhook_send instead.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "text", Label: "Message text (overrides params.text)"},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Posted-message metadata (channel, ts, permalink)", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"token":     {"type":"string","description":"Slack bot token. ALWAYS use ${secret:NAME} — never paste xoxb-... literals."},
					"channel":   {"type":"string","description":"Channel ID (CXXXXX), name (#general), or user ID (UXXXXX)."},
					"text":      {"type":"string","description":"Message text. Used when no input port is wired."},
					"thread_ts": {"type":"string","description":"Reply in this thread instead of as a top-level message."},
					"blocks":    {"description":"Optional Slack Block Kit blocks (array). When present, text becomes the fallback for notifications."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"HTTP timeout in milliseconds."}
				},
				"required":["token","channel"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeSlackPost,
	})
}

// executeSlackPost calls chat.postMessage and maps Slack's quirky
// transport into the engine's outcome model.
//
// Two failure surfaces the caller cares about:
//   - HTTP-level: connection refused, non-2xx status. These come back
//     as "send_failed" / "slack_http_error".
//   - API-level: Slack returns 200 OK with `{"ok": false, "error": "..."}`
//     for invalid_auth, channel_not_found, rate_limited, etc. These
//     come back as "slack_api_error" with the error code so dashboards
//     can group on it.
func executeSlackPost(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	token, err := paramString(job.Params, "token")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	if !strings.HasPrefix(token, "xox") {
		// Loose sanity check — Slack bot tokens are xoxb-..., user
		// tokens xoxp-... Refuse anything that doesn't look like a
		// Slack token so a misconfigured ${secret:...} (resolved to
		// "" or to the wrong secret) fails fast with a clear message
		// instead of returning a confusing 401 from Slack.
		return errResult(job, "bad_param", "token does not look like a Slack token (expected xox...)"), nil
	}
	channel, err := paramString(job.Params, "channel")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}

	// Text resolution: input port beats params; missing entirely is OK
	// only when blocks are supplied — Slack rejects a message with no
	// text AND no blocks.
	var text string
	if input, ok := job.Input["text"]; ok && input.Inline != nil {
		if s, ok := input.Inline.(string); ok {
			text = s
		} else {
			b, _ := json.Marshal(input.Inline)
			text = string(b)
		}
	} else {
		text = paramStringDefault(job.Params, "text", "")
	}
	blocks, hasBlocks := job.Params["blocks"]
	if text == "" && !hasBlocks {
		return errResult(job, "bad_param", "either text or blocks must be set"), nil
	}

	payload := map[string]any{"channel": channel}
	if text != "" {
		payload["text"] = text
	}
	if hasBlocks {
		payload["blocks"] = blocks
	}
	if thread := paramStringDefault(job.Params, "thread_ts", ""); thread != "" {
		payload["thread_ts"] = thread
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errResult(job, "marshal_payload", err.Error()), nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slackPostURL, bytes.NewReader(body))
	if err != nil {
		return errResult(job, "bad_url", err.Error()), nil
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)

	timeoutMs := paramIntDefault(job.Params, "timeout_ms", 15000)
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}

	emitProgress(progress, job, 0.3, "POST chat.postMessage")
	resp, err := client.Do(req)
	if err != nil {
		return errResult(job, "send_failed", err.Error()), nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errResult(job, "slack_http_error",
			fmt.Sprintf("slack returned %d: %s",
				resp.StatusCode, strings.TrimSpace(string(respBody)))), nil
	}

	// Slack's "API-level" failure shape — 200 OK with ok:false. We pull
	// the canonical error code out so retries can target retryable
	// errors (e.g. rate_limited) and surface the rest as terminal.
	var apiResp struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Warning string `json:"warning"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
		Message struct {
			Permalink string `json:"permalink"`
		} `json:"message"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return errResult(job, "slack_bad_response", "decode slack response: "+err.Error()), nil
	}
	if !apiResp.OK {
		return errResult(job, "slack_api_error", apiResp.Error), nil
	}

	emitProgress(progress, job, 1.0, "posted ts="+apiResp.TS)
	meta := map[string]any{
		"channel":   apiResp.Channel,
		"ts":        apiResp.TS,
		"permalink": apiResp.Message.Permalink,
	}
	if apiResp.Warning != "" {
		meta["warning"] = apiResp.Warning
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
}
