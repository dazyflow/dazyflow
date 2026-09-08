// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"reflect"
	"testing"
)

func TestWebhookSecrets_Cov(t *testing.T) {
	got := WebhookSecrets(map[string]any{"secrets": []string{" a ", "", "b"}})
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("[]string form = %v, want %v", got, want)
	}
	got = WebhookSecrets(map[string]any{"secrets": []any{"x", 42, " y "}})
	if want := []string{"x", "y"}; !reflect.DeepEqual(got, want) {
		t.Errorf("[]any form = %v, want %v", got, want)
	}
	if got := WebhookSecrets(map[string]any{}); got != nil {
		t.Errorf("missing key should yield nil, got %v", got)
	}
	if got := WebhookSecrets(map[string]any{"secrets": "single"}); got != nil {
		t.Errorf("scalar secrets should yield nil, got %v", got)
	}
}

func TestGraphWebhookSecrets(t *testing.T) {
	g := Graph{Nodes: []Node{
		{Module: "webhook_input", Params: map[string]any{"secrets": []string{"k1"}}},
		{Module: "noop", Params: map[string]any{"secrets": []string{"ignored"}}},
		{Module: "webhook_input", Params: map[string]any{"secrets": []string{"k2", "k3"}}},
	}}
	got := GraphWebhookSecrets(g)
	want := []string{"k1", "k2", "k3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GraphWebhookSecrets = %v, want %v", got, want)
	}
	if got := GraphWebhookSecrets(Graph{}); got != nil {
		t.Errorf("empty graph should yield nil, got %v", got)
	}
}

// A public webhook step makes the flow live: it can receive, so reporting it
// as manual-only would be a lie the chip tells on every canvas.
func TestFlowStatus_PublicWebhookCountsAsLive(t *testing.T) {
	g := Graph{Nodes: []Node{
		{ID: "in", Module: WebhookInputModule, Params: map[string]any{"public": true}},
	}}
	if !HasConfiguredAutoTrigger(g) {
		t.Error("a public webhook step should count as a configured trigger")
	}
}

func TestFlowStatus_KeylessPrivateWebhookIsNotLive(t *testing.T) {
	g := Graph{Nodes: []Node{
		{ID: "in", Module: WebhookInputModule, Params: map[string]any{}},
	}}
	if HasConfiguredAutoTrigger(g) {
		t.Error("a key-less, non-public webhook step is inert and must not count")
	}
}

// The lint's job is to catch a step that cannot receive. A public one can, so
// telling its author to generate a key would be wrong.
func TestTriggerLint_PublicWebhookIsNotFlagged(t *testing.T) {
	g := Graph{Nodes: []Node{
		{ID: "in", Module: WebhookInputModule, Params: map[string]any{"public": true}},
	}}
	for _, issue := range lintTriggers(g) {
		if issue.Code == "trigger_webhook_no_secret" {
			t.Errorf("public step flagged as unable to receive: %+v", issue)
		}
	}
}

func TestWebhookPublic_DefaultsOff(t *testing.T) {
	if WebhookPublic(map[string]any{}) {
		t.Error("public must default to off — a fresh Webhook step has no keys either")
	}
	if WebhookPublic(map[string]any{"public": false}) {
		t.Error("public:false is off")
	}
	if !WebhookPublic(map[string]any{"public": true}) {
		t.Error("public:true is on")
	}
}

func TestGraphWebhookPublic_OneOpenStepOpensTheAddress(t *testing.T) {
	g := Graph{Nodes: []Node{
		{ID: "a", Module: WebhookInputModule, Params: map[string]any{"secrets": []any{"k"}}},
		{ID: "b", Module: WebhookInputModule, Params: map[string]any{"public": true}},
	}}
	if !GraphWebhookPublic(g) {
		t.Error("a graph with any public webhook step is public")
	}
}

func TestFlowStatus_PublicRequestCountsAsLive(t *testing.T) {
	g := Graph{Nodes: []Node{
		{ID: "in", Module: RequestInputModule, Params: map[string]any{"public": true}},
	}}
	if !HasConfiguredAutoTrigger(g) {
		t.Error("a public Request step should count as a configured trigger")
	}
}

func TestFlowStatus_KeylessPrivateRequestIsNotLive(t *testing.T) {
	g := Graph{Nodes: []Node{
		{ID: "in", Module: RequestInputModule, Params: map[string]any{}},
	}}
	if HasConfiguredAutoTrigger(g) {
		t.Error("a key-less, non-public Request step is inert and must not count")
	}
}

func TestTriggerLint_PublicRequestIsNotFlagged(t *testing.T) {
	g := Graph{Nodes: []Node{
		{ID: "in", Module: RequestInputModule, Params: map[string]any{"public": true}},
		{ID: "out", Module: ReplyModule},
	}}
	for _, issue := range lintTriggers(g) {
		if issue.Code == "trigger_request_no_secret" {
			t.Errorf("public Request step flagged as unable to receive: %+v", issue)
		}
	}
}

func TestGraphRequestPublic_OneOpenStepOpensTheAddress(t *testing.T) {
	g := Graph{Nodes: []Node{
		{ID: "a", Module: RequestInputModule, Params: map[string]any{"secrets": []any{"k"}}},
		{ID: "b", Module: RequestInputModule, Params: map[string]any{"public": true}},
	}}
	if !GraphRequestPublic(g) {
		t.Error("a graph with any public Request step is public")
	}
	// A public Webhook step must not open /call, and vice versa: the two
	// addresses are separate doors with separate promises.
	w := Graph{Nodes: []Node{
		{ID: "a", Module: WebhookInputModule, Params: map[string]any{"public": true}},
	}}
	if GraphRequestPublic(w) {
		t.Error("a public Webhook step must not open the /call address")
	}
	if GraphWebhookPublic(g) {
		t.Error("a public Request step must not open the /trigger address")
	}
}
