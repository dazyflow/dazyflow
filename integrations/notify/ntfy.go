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
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "ntfy",
			Version:        "1.0",
			Label:          "ntfy",
			Color:          "#52bca6",
			Icon:           "ntfy",
			Category:       "network",
			Provider:       "internal",
			Integration:    "ntfy",
			Tags:           []string{"ntfy", "push", "notify", "report"},
			Description:    "Push a notification through ntfy.sh (or a self-hosted ntfy server). Set the server, the topic, and the message. Optional title, priority, tags, and a click-URL attach extras for richer notifications on the receiving device.",
			Summary:        "Publish a push notification to an ntfy topic with optional title, priority, tags, click-URL, and bearer token.",
			Examples: []core.ParamsExample{
				{
					Title:  "Simple message to a public topic",
					Params: json.RawMessage(`{"topic":"my-alerts","message":"Build #1234 succeeded."}`),
				},
				{
					Title:  "High-priority alert on a self-hosted server",
					Params: json.RawMessage(`{"server":"https://ntfy.example.com","topic":"oncall","title":"Pager","message":"Error rate above 5%","priority":"high","tags":["warning","rotating_light"],"click":"https://dashboard.example.com/alerts","token":"${secret:NTFY_TOKEN}"}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "body", Label: "Message body (overrides params.message)"},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Delivery metadata (JSON)"},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"server":{"type":"string","default":"https://ntfy.sh","description":"ntfy server base URL. Use your self-hosted server (e.g. https://ntfy.klahr.se) for private topics."},
						"topic":{"type":"string","description":"Topic name. Anyone subscribed to {server}/{topic} receives the notification."},
						"message":{"type":"string","description":"Notification body. Overridden by the body input port if connected."},
						"title":{"type":"string","description":"Optional title (rendered as the notification headline)."},
						"priority":{"type":"string","enum":["min","low","default","high","max"],"default":"default","description":"Notification priority. max emits urgent alerts on most platforms."},
						"tags":{"type":"array","items":{"type":"string"},"description":"Emoji shortcodes or words shown next to the title (e.g. [\"warning\",\"penguin\"])."},
						"click":{"type":"string","description":"Optional URL to open when the notification is tapped."},
						"token":{"type":"string","description":"Bearer token for authenticated topics. Use ${env:NAME} to pull from a secret."},
						"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"HTTP timeout in milliseconds."}
					},
					"required":["topic"]
				}`,
			),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeNtfy,
	})
}

func executeNtfy(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	topic, err := params.String(job.Params, "topic")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	server := params.StringDefault(job.Params, "server", "https://ntfy.sh")
	server = strings.TrimRight(server, "/")

	body := params.StringDefault(job.Params, "message", "")
	if input, ok := job.Input["body"]; ok {
		switch v := input.Inline.(type) {
		case string:
			if v != "" {
				body = v
			}
		case []byte:
			if len(v) > 0 {
				body = string(v)
			}
		case nil:
			// fall through
		default:
			raw, mErr := json.MarshalIndent(v, "", "  ")
			if mErr != nil {
				return params.Err(job, "bad_input", mErr.Error()), nil
			}
			body = string(raw)
		}
	}

	url := server + "/" + topic
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		bytes.NewReader([]byte(body)))
	if err != nil {
		return params.Err(job, "bad_url", err.Error()), nil
	}

	if title := params.StringDefault(job.Params, "title", ""); title != "" {
		req.Header.Set("Title", title)
	}
	if priority := params.StringDefault(job.Params, "priority", ""); priority != "" {
		req.Header.Set("Priority", priority)
	}
	if tags := paramStringSlice(job.Params, "tags"); len(tags) > 0 {
		req.Header.Set("Tags", strings.Join(tags, ","))
	}
	if click := params.StringDefault(job.Params, "click", ""); click != "" {
		req.Header.Set("Click", click)
	}
	if token := params.StringDefault(job.Params, "token", ""); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	timeoutMs := params.IntDefault(job.Params, "timeout_ms", 15000)
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}

	emitProgress(progress, job, 0.3, "POST "+url)
	resp, err := client.Do(req)
	if err != nil {
		return params.Err(job, "send_failed", err.Error()), nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return params.Err(job, "ntfy_error",
			fmt.Sprintf("ntfy returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))), nil
	}
	emitProgress(progress, job, 1.0, fmt.Sprintf("delivered (%d)", resp.StatusCode))

	meta := map[string]any{
		"server":     server,
		"topic":      topic,
		"url":        url,
		"status":     resp.StatusCode,
		"bytes_sent": len(body),
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
}
