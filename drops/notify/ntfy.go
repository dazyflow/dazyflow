package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "ntfy",
			Version:     "1.0",
			Label:       "ntfy push",
			Summary:     "Send a push notification to an ntfy topic (ntfy.sh or self-hosted).",
			Description: "Publish a push notification to an ntfy topic. The message comes from the 'body' input or params.message; title, priority, tags and a click URL are optional. Works with ntfy.sh or any self-hosted ntfy server.",
			Integration: "ntfy",
			Category:    "network",
			Icon:        "ntfy",
			Color:       "#52bca6",
			Provider:    "internal",
			Tags:        []string{"ntfy", "push", "notify", "notification", "alert", "phone", "reminder", "message", "ping"},
			Examples: []core.ParamsExample{
				{Title: "Alert to a topic", Params: json.RawMessage(`{"topic":"my-alerts","title":"Deploy done","message":"main is green","priority":"4","tags":["white_check_mark"]}`)},
			},
			// The server endpoint + access token are a per-tenant connection
			// set once on the integration page (default is the public ntfy.sh);
			// flows then only carry the per-notification 'topic'/'message'.
			ConnectionFields: []core.ConnectionField{
				{Key: "server", Label: "Server URL", Placeholder: "https://ntfy.sh"},
				{Key: "token", Label: "Access token", Secret: true, Placeholder: "tk_… (for protected topics)"},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "body", Label: "Message"},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Delivery metadata", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"server":{"type":"string","default":"https://ntfy.sh","description":"ntfy server base URL."},
					"topic":{"type":"string","description":"A name you pick for this notification channel — letters, numbers, dashes or underscores, no spaces. To receive the messages, subscribe to this same topic in the ntfy app or open ntfy.sh/<your-topic> in a browser.","examples":["my-daily-hello"]},
					"message":{"type":"string","description":"The text to send. Optional if you connect a Message input from another step.","examples":["Hello"]},
					"title":{"type":"string","description":"Notification title."},
					"priority":{"type":"string","enum":["1","2","3","4","5"],"enumNames":["1 — Min","2 — Low","3 — Default","4 — High","5 — Max"],"description":"How urgently it buzzes. Leave unset for the normal level."},
					"tags":{"type":"array","items":{"type":"string"},"description":"Emoji/tag shortcodes."},
					"click":{"type":"string","description":"URL opened when the notification is tapped."},
					"token":{"type":"string","description":"Bearer token for protected topics."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["topic"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeNtfy,
	})
}

func executeNtfy(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	topic, _ := params.StringOpt(job.Params, "topic")
	if topic == "" {
		return params.Err(job, "bad_param", "'topic' is required"), nil
	}
	server := strings.TrimRight(params.StringDefault(job.Params, "server", "https://ntfy.sh"), "/")

	body := params.StringDefault(job.Params, "message", "")
	if in, ok := job.Input["body"]; ok && in.Inline != nil {
		switch v := in.Inline.(type) {
		case string:
			body = v
		case []byte:
			body = string(v)
		default:
			if b, err := json.MarshalIndent(v, "", "  "); err == nil {
				body = string(b)
			}
		}
	}

	url := server + "/" + topic
	if err := hfnet.EgressAllowed(url); err != nil {
		return params.Err(job, "egress_blocked", err.Error()), nil
	}

	timeout := time.Duration(params.IntDefault(job.Params, "timeout_ms", 15000)) * time.Millisecond
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "POST", url, bytes.NewReader([]byte(body)))
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if v, _ := params.StringOpt(job.Params, "title"); v != "" {
		req.Header.Set("Title", v)
	}
	if v, _ := params.StringOpt(job.Params, "priority"); v != "" {
		req.Header.Set("Priority", v)
	}
	if tags := paramTags(job.Params); tags != "" {
		req.Header.Set("Tags", tags)
	}
	if v, _ := params.StringOpt(job.Params, "click"); v != "" {
		req.Header.Set("Click", v)
	}
	if v, _ := params.StringOpt(job.Params, "token"); v != "" {
		req.Header.Set("Authorization", "Bearer "+v)
	}

	resp, err := hfnet.SafeHTTPClient(timeout, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return params.Err(job, "ntfy_http_error", err.Error()), nil
	}
	defer resp.Body.Close()
	text, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := string(text)
		if len(detail) > 512 {
			detail = detail[:512]
		}
		return params.Err(job, "ntfy_error", "ntfy returned "+resp.Status+": "+detail), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"meta": {MIME: "application/json", Inline: map[string]any{
			"server": server, "topic": topic, "url": url, "status": resp.StatusCode, "bytes_sent": len(body),
		}}},
	}, nil
}

func paramTags(p map[string]any) string {
	v, ok := p["tags"]
	if !ok {
		return ""
	}
	switch arr := v.(type) {
	case []string:
		return strings.Join(arr, ",")
	case []any:
		out := make([]string, 0, len(arr))
		for _, it := range arr {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return strings.Join(out, ",")
	}
	return ""
}
