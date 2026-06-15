package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "ai_draft_reply",
			Version:     "1.0",
			Label:       "Claude",
			Subtitle:    "Draft reply",
			Summary:     "Write a suggested reply to an incoming message.",
			Description: "Feed in an incoming email or message and get a ready-to-send draft back. Choose the tone and add any guidance (\"offer a refund\", \"point them to the docs\"). Pair with Await approval before actually sending.",
			Integration: "Claude",
			Category:    "ai",
			Icon:        "claude",
			Color:       "#cc7755",
			Provider:    "internal",
			Tags:        []string{"ai", "claude", "reply", "draft", "email", "response", "message"},
			Examples: []core.ParamsExample{
				{Title: "Friendly support reply", Params: json.RawMessage(`{"tone":"friendly","guidance":"Apologize for the delay and offer to help further."}`), Notes: "Wire the incoming message into the Text input; wire the Reply output into Await approval, then an email/Slack send."},
			},
			ConnectionFields: connectionFields,
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "text", Label: "Message"},
			},
			Outputs: []core.Port{
				{Port: "reply", Label: "Reply", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"text":{"type":"string","format":"multiline","title":"Message to reply to","description":"Or wire it into the Message input."},
					"tone":{"type":"string","title":"Tone","enum":["friendly","formal","concise","apologetic"],"enumNames":["Friendly","Formal","Concise","Apologetic"],"default":"friendly"},
					"guidance":{"type":"string","format":"multiline","title":"Anything to include?","description":"e.g. 'offer a 10% refund', 'point them to the help docs'."},
					"language":{"type":"string","title":"Reply language","description":"Leave blank to match the message.","x_advanced":true},
					"signature":{"type":"string","format":"multiline","title":"Signature","description":"Appended to the end of the reply.","x_advanced":true},
					"model":{"type":"string","x_advanced":true,"description":"Claude model id.","default":"claude-sonnet-4-6"},
					"api_key":{"type":"string","x_advanced":true,"description":"Injected from the Claude connection — leave unset."},
					"base_url":{"type":"string","x_advanced":true,"description":"Override the API host."},
					"timeout_ms":{"type":"integer","default":60000,"minimum":1}
				}
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeDraftReply,
	})
}

func executeDraftReply(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	text := resolveText(job)
	tone := params.StringDefault(job.Params, "tone", "friendly")
	guidance, _ := params.StringOpt(job.Params, "guidance")
	language, _ := params.StringOpt(job.Params, "language")
	signature, _ := params.StringOpt(job.Params, "signature")

	temp := 0.5
	out, je := callClaude(ctx, job, callOpts{
		system:      draftReplySystem(tone, guidance, language),
		userText:    text,
		temperature: &temp,
		model:       params.StringDefault(job.Params, "model", defaultModel),
		timeoutMS:   params.IntDefault(job.Params, "timeout_ms", 60000),
	})
	if je != nil {
		return params.Err(job, je.Code, je.Message), nil
	}

	reply := strings.TrimSpace(out.text)
	if sig := strings.TrimSpace(signature); sig != "" {
		reply = reply + "\n\n" + sig
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"reply":    {MIME: "text/plain", Inline: reply},
			"response": {MIME: "application/json", Inline: out.raw},
		},
	}, nil
}

func draftReplySystem(tone, guidance, language string) string {
	var b strings.Builder
	b.WriteString("You are drafting a reply to the message the user provides. ")
	switch tone {
	case "formal":
		b.WriteString("Write in a formal, professional tone. ")
	case "concise":
		b.WriteString("Write in a brief, to-the-point tone. ")
	case "apologetic":
		b.WriteString("Write in a warm, apologetic tone. ")
	default:
		b.WriteString("Write in a friendly, helpful tone. ")
	}
	if g := strings.TrimSpace(guidance); g != "" {
		fmt.Fprintf(&b, "Follow this guidance: %s. ", g)
	}
	if lang := strings.TrimSpace(language); lang != "" {
		fmt.Fprintf(&b, "Write the reply in %s. ", lang)
	}
	b.WriteString("Output only the reply body — no subject line, no signature, and no preamble like \"Here is a draft\".")
	return b.String()
}
