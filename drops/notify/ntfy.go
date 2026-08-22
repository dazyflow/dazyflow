// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"unicode/utf8"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "ntfy",
			Version:     "1.0",
			Label:       "ntfy",
			Subtitle:    "Send notification",
			Summary:     "Send a push notification to your phone via an ntfy topic.",
			Description: "Send a push notification to an ntfy topic — subscribe to the same topic in the free ntfy app to receive it on your phone. The message can be typed on the step or connected in from another step; title, priority, tags and a tap link are optional. Defaults to the public ntfy.sh server; to use a self-hosted ntfy server, set its Server URL (and a token for protected topics) once via the ntfy connection (Apps page, or the configure_connection tool) — flows then only carry the per-notification topic and message.",
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
				// Named after their params so the card shows inline editable
				// boxes (Unreal-style); a wired value overrides the typed one.
				{Port: "title", Label: "Title", MIME: []string{"text/plain"}},
				{Port: "message", Label: "Message", MIME: []string{"text/plain"}},
				// The tap link is usually computed upstream — an approval
				// step's link, a run's report URL — so it takes a wire, not
				// just a typed value.
				{Port: "click", Label: "Link to open", MIME: []string{"text/plain"}},
			},
			// No declared outputs: sending a notification is a "do" step —
			// chain via the pass-through pin. The delivery details are still
			// EMITTED under "meta" for run records, just not a pin (same as
			// gmail send / sheets append).
			Outputs: []core.Port{
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			// server + token are NOT params: they're the per-tenant connection
			// (ConnectionFields above), injected into unset params at run time
			// by injectConnectionDefaults. Declaring them here too would render
			// confusing always-on node fields AND let a node value shadow the
			// tenant connection (paramFilled skips injection) — and put a raw
			// bearer token in the graph in plaintext. Flows carry only the
			// per-notification fields.
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"topic":{"type":"string","title":"Topic","description":"A name you pick for this notification channel — letters, numbers, dashes or underscores, no spaces. To receive the messages, subscribe to this same topic in the ntfy app or open ntfy.sh/<your-topic> in a browser.","examples":["my-daily-hello"]},
					"message":{"type":"string","title":"Message","description":"The text to send. Optional if you connect a Message input from another step.","examples":["Hello"]},
					"title":{"type":"string","title":"Title","description":"Notification title."},
					"priority":{"type":"string","title":"Priority","enum":["1","2","3","4","5"],"enumNames":["1 — Min","2 — Low","3 — Default","4 — High","5 — Max"],"description":"How urgently it buzzes. Leave unset for the normal level."},
					"tags":{"type":"array","title":"Tags","items":{"type":"string"},"description":"Emoji/tag shortcodes."},
					"click":{"type":"string","title":"Link to open","description":"Web address opened when the notification is tapped. Connect an Await approval step's 'Approval link' into the matching input so the recipient can approve straight from the notification."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["topic"]
			}`),
			Idempotent: false,
			// ntfy's publish endpoint has no generic idempotency header, so
			// a retried POST sends the notification twice. This drop is a
			// terminal leaf the engine auto-retries on backoff, so retries
			// must be off here.
			RetryPolicy: core.RetryNever,
			// A non-idempotent external write: opt into engine-side dedupe so an
			// expired-lease reclaim or crash recovery replays the recorded result
			// instead of firing the write a second time. Matches the other
			// send-style drops (discord/gmail/sheets/twilio/klarna/nshift/elks).
			DedupeWrites: true,
		},
		Execute: executeNtfy,
	})
}

func executeNtfy(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	topic, _ := params.StringOpt(job.Params, "topic")
	if topic == "" {
		return params.Err(job, "bad_param", "'topic' is required"), nil
	}
	server := strings.TrimRight(params.StringDefault(job.Params, "server", "https://ntfy.sh"), "/")

	body := params.StringDefault(job.Params, "message", "")
	// The Message input overrides the param.
	in, ok := job.Input["message"]
	if ok && in.Inline != nil {
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

	// ntfy treats a body over its message limit (~4 KiB) as a file-attachment
	// upload, which public servers reject ("attachments not allowed"). A push
	// notification is a glance, not a document — truncate long input (e.g. a
	// whole email body wired into Message) instead of failing the run. We
	// truncate rather than fail, but say so loudly (progress + result meta)
	// so a long wired body doesn't silently lose its tail.
	const ntfyMaxMessage = 4000
	origBytes := len(body)
	truncated := origBytes > ntfyMaxMessage
	if truncated {
		cut := ntfyMaxMessage
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut-- // don't split a multi-byte character (å, emoji, …)
		}
		body = body[:cut] + "…"
		emitProgress(progress, job, 0.3, fmt.Sprintf(
			"message was %d bytes; ntfy caps notifications at ~%d, so it was shortened — connect a summary instead of a full document if you need the whole thing.",
			origBytes, ntfyMaxMessage))
	}

	url := server + "/" + topic
	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
		return params.Err(job, "egress_blocked", err.Error()), nil
	}

	timeout := time.Duration(params.IntDefault(job.Params, "timeout_ms", 15000)) * time.Millisecond
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "POST", url, bytes.NewReader([]byte(body)))
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	title, _ := params.StringOpt(job.Params, "title")
	// The Title input overrides the param when wired.
	if in, ok := job.Input["title"]; ok && in.Inline != nil {
		if s, isStr := in.Inline.(string); isStr && s != "" {
			title = s
		}
	}
	if title != "" {
		req.Header.Set("Title", title)
	}
	if v, _ := params.StringOpt(job.Params, "priority"); v != "" {
		req.Header.Set("Priority", v)
	}
	if tags := paramTags(job.Params); tags != "" {
		req.Header.Set("Tags", tags)
	}
	click, ok := params.TextInputOr(job, "click", params.StringDefault(job.Params, "click", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Link to open' input must be text"), nil
	}
	if click != "" {
		req.Header.Set("Click", click)
	}
	if v, _ := params.StringOpt(job.Params, "token"); v != "" {
		req.Header.Set("Authorization", "Bearer "+v)
	}

	resp, err := hfnet.SafeHTTPClient(timeout, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return params.Err(job, "ntfy_http_error", err.Error()), nil
	}
	defer resp.Body.Close()
	// Cap the response read like the other notify helpers (verify.go uses
	// 1<<16) so a hostile/buggy upstream can't stream an unbounded body —
	// we only need the first chunk for the error detail below anyway.
	const maxResponseBytes = 1 << 16 // 64 KiB
	text, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
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
			"server": server, "topic": topic, "url": url, "status": resp.StatusCode,
			"bytes_sent": len(body), "truncated": truncated, "original_bytes": origBytes,
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
