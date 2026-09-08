// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func webhookGraph() core.Graph {
	return core.Graph{
		Nodes: []core.Node{
			{ID: "webhook_input_1", Module: "webhook_input"},
			{ID: "email_send_1", Module: "email_send"},
		},
	}
}

func firedWebhook() map[string]core.Result {
	return map[string]core.Result{
		"webhook_input_1": {Output: map[string]core.Ref{
			"body": {Inline: map[string]any{"version": "0.27.3", "actor": "ci"}},
		}},
	}
}

func TestTrigger_ResolvesAgainstTheNodeTheRunStartedFrom(t *testing.T) {
	job := &core.Job{Params: map[string]any{
		"body": "Version <b>${trigger.body.version}</b> has been released!",
	}}
	if err := resolveTemplates(t.Context(), nil, webhookGraph(), firedWebhook(), job); err != nil {
		t.Fatalf("resolveTemplates: %v", err)
	}
	want := "Version <b>0.27.3</b> has been released!"
	if got := job.Params["body"]; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestTrigger_AgreesWithTheUpstreamFormOfTheSameValue(t *testing.T) {
	// The two schemes name one thing. If they ever disagree about path syntax
	// or stringification, the friendlier name is the one people will have
	// written, and it is the one that would be wrong.
	job := &core.Job{Params: map[string]any{
		"a": "${trigger.body.version}",
		"b": "${upstream.webhook_input_1.body.version}",
	}}
	if err := resolveTemplates(t.Context(), nil, webhookGraph(), firedWebhook(), job); err != nil {
		t.Fatalf("resolveTemplates: %v", err)
	}
	if job.Params["a"] != job.Params["b"] {
		t.Errorf("trigger form = %q, upstream form = %q — these must agree",
			job.Params["a"], job.Params["b"])
	}
}

func TestTrigger_PicksTheTriggerThatActuallyFired(t *testing.T) {
	g := core.Graph{Nodes: []core.Node{
		{ID: "cron_trigger_1", Module: "cron_trigger"},
		{ID: "webhook_input_1", Module: "webhook_input"},
	}}
	job := &core.Job{Params: map[string]any{"v": "${trigger.body.version}"}}
	if err := resolveTemplates(t.Context(), nil, g, firedWebhook(), job); err != nil {
		t.Fatalf("resolveTemplates: %v", err)
	}
	if got := job.Params["v"]; got != "0.27.3" {
		t.Errorf("v = %q, want the FIRED trigger's body", got)
	}
}

func TestTrigger_FailsLoudlyWhenNoTriggerFired(t *testing.T) {
	job := &core.Job{Params: map[string]any{"v": "${trigger.body.version}"}}
	err := resolveTemplates(t.Context(), nil, webhookGraph(), map[string]core.Result{}, job)
	if err == nil {
		t.Fatalf("want an error, got v = %q", job.Params["v"])
	}
	if !strings.Contains(err.Error(), "no trigger fired") {
		t.Errorf("error should name the cause: %v", err)
	}
	if !strings.Contains(err.Error(), "Send test event") {
		t.Errorf("error should point at the way to run this flow by hand: %v", err)
	}
}

// The whole point of the scheme: it must work from ANY node in the run, not
// only one wired straight into the trigger. The engine reads whatever results
// it is handed; daemon/template_refs.go is what puts the trigger there for a
// node further down the chain.
func TestTrigger_ResolvesFromAnywhereInTheRun(t *testing.T) {
	g := core.Graph{Nodes: []core.Node{
		{ID: "webhook_input_1", Module: "webhook_input"},
		{ID: "if_1", Module: "if"},
		{ID: "email_send_1", Module: "email_send"},
	}}
	prior := map[string]core.Result{
		"if_1":            {Output: map[string]core.Ref{"then": {Inline: "yes"}}},
		"webhook_input_1": {Output: map[string]core.Ref{"body": {Inline: map[string]any{"version": "0.27.5"}}}},
	}
	job := &core.Job{Params: map[string]any{"body": "Version ${trigger.body.version} shipped"}}
	if err := resolveTemplates(t.Context(), nil, g, prior, job); err != nil {
		t.Fatalf("resolveTemplates: %v", err)
	}
	if got := job.Params["body"]; got != "Version 0.27.5 shipped" {
		t.Errorf("body = %q", got)
	}
}

func TestTrigger_ResolvesTheWholeBodyPort(t *testing.T) {
	job := &core.Job{Params: map[string]any{"v": "${trigger.body}"}}
	if err := resolveTemplates(t.Context(), nil, webhookGraph(), firedWebhook(), job); err != nil {
		t.Fatalf("resolveTemplates: %v", err)
	}
	if got, _ := job.Params["v"].(string); got == "${trigger.body}" || got == "" {
		t.Errorf("v = %q, want the body rendered", got)
	}
}

func TestIsTriggerModule(t *testing.T) {
	for _, m := range []string{"webhook_input", "cron_trigger", "poll_trigger",
		"google_form_trigger", "github_on_push", "stripe_on_payment"} {
		if !core.IsTriggerModule(m) {
			t.Errorf("%s should be a trigger module", m)
		}
	}
	for _, m := range []string{"email_send", "if", "http_request"} {
		if core.IsTriggerModule(m) {
			t.Errorf("%s is not a trigger module", m)
		}
	}
}
