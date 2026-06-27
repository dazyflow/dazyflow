// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"
)

func TestFlowRunStatusOf(t *testing.T) {
	pollNode := func(secs any) Node {
		return Node{ID: "p", Module: "google_form_trigger", Params: map[string]any{"form_id": "f", "interval_seconds": secs}}
	}
	cases := []struct {
		name string
		g    Graph
		want FlowRunStatus
	}{
		{
			name: "no triggers — manual only",
			g:    Graph{Nodes: []Node{{ID: "a", Module: "http_request"}}},
			want: FlowManual,
		},
		{
			name: "google form with positive interval — live",
			g:    Graph{Nodes: []Node{pollNode(float64(60))}},
			want: FlowLive,
		},
		{
			name: "google form with zero interval — manual (blank = run on demand)",
			g:    Graph{Nodes: []Node{pollNode(float64(0))}},
			want: FlowManual,
		},
		{
			// The bug that started this: a string "60" is what a naive form
			// input emits, and the scheduler's paramSeconds rejects it. The
			// chip must agree — a string interval is NOT live.
			name: "google form with string interval — manual (scheduler ignores strings)",
			g:    Graph{Nodes: []Node{pollNode("60")}},
			want: FlowManual,
		},
		{
			name: "google form over the ceiling — manual (scheduler refuses it)",
			g:    Graph{Nodes: []Node{pollNode(float64(MaxPollIntervalSeconds + 1))}},
			want: FlowManual,
		},
		{
			name: "configured cron node — live",
			g:    Graph{Nodes: []Node{{ID: "c", Module: "cron_trigger", Params: map[string]any{"cron": "0 9 * * *"}}}},
			want: FlowLive,
		},
		{
			name: "blank cron node — manual",
			g:    Graph{Nodes: []Node{{ID: "c", Module: "cron_trigger", Params: map[string]any{"cron": "  "}}}},
			want: FlowManual,
		},
		{
			name: "graph-level cron — live",
			g:    Graph{Triggers: []GraphTrigger{{Type: "cron", Cron: "*/5 * * * *"}}},
			want: FlowLive,
		},
		{
			name: "webhook input with secret — live",
			g:    Graph{Nodes: []Node{{ID: "w", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}}}}},
			want: FlowLive,
		},
		{
			name: "webhook input, no secret, no form — manual (unreachable)",
			g:    Graph{Nodes: []Node{{ID: "w", Module: "webhook_input", Params: map[string]any{}}}},
			want: FlowManual,
		},
		{
			name: "disabled wins over a configured trigger — paused",
			g:    Graph{Disabled: true, Nodes: []Node{pollNode(float64(60))}},
			want: FlowPaused,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FlowRunStatusOf(tc.g); got != tc.want {
				t.Errorf("FlowRunStatusOf = %q, want %q", got, tc.want)
			}
		})
	}
}

func cronTriggerNode(expr string) Node {
	return Node{ID: "t", Module: "cron_trigger", Params: map[string]any{"cron": expr}}
}

func webhookNode(secret string) Node {
	return Node{ID: "w", Module: "webhook_input", Params: map[string]any{"secrets": []string{secret}}}
}

func TestHasConfiguredSchedulerTrigger(t *testing.T) {
	// Graph-level cron counts.
	if !HasConfiguredSchedulerTrigger(Graph{Triggers: []GraphTrigger{{Type: "cron", Cron: "0 9 * * *"}}}) {
		t.Error("graph-level cron should be a scheduler trigger")
	}
	// cron_trigger node counts.
	if !HasConfiguredSchedulerTrigger(Graph{Nodes: []Node{cronTriggerNode("* * * * *")}}) {
		t.Error("cron_trigger node should be a scheduler trigger")
	}
	// poll_trigger with valid interval counts.
	poll := Graph{Nodes: []Node{{Module: "poll_trigger", Params: map[string]any{"interval_seconds": float64(300)}}}}
	if !HasConfiguredSchedulerTrigger(poll) {
		t.Error("poll_trigger with interval should be a scheduler trigger")
	}
	// A webhook-only flow is NOT a scheduler trigger.
	if HasConfiguredSchedulerTrigger(Graph{Nodes: []Node{webhookNode("k")}}) {
		t.Error("webhook should not count as a scheduler trigger")
	}
	// Empty cron expression does not count.
	if HasConfiguredSchedulerTrigger(Graph{Nodes: []Node{cronTriggerNode("  ")}}) {
		t.Error("blank cron should not count")
	}
}

func TestFlowRunStatusPublished(t *testing.T) {
	schedFlow := Graph{Nodes: []Node{cronTriggerNode("0 9 * * *")}}
	webhookFlow := Graph{Nodes: []Node{webhookNode("k")}}
	manualFlow := Graph{Nodes: []Node{{Module: "noop"}}}

	tests := []struct {
		name      string
		g         Graph
		published bool
		want      FlowRunStatus
	}{
		{"disabled wins", Graph{Disabled: true, Nodes: schedFlow.Nodes}, false, FlowPaused},
		{"scheduler unpublished needs publish", schedFlow, false, FlowNeedsPublish},
		{"scheduler published is live", schedFlow, true, FlowLive},
		{"webhook unpublished stays live", webhookFlow, false, FlowLive},
		{"manual", manualFlow, false, FlowManual},
		{"manual published", manualFlow, true, FlowManual},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FlowRunStatusPublished(tt.g, tt.published); got != tt.want {
				t.Errorf("FlowRunStatusPublished = %q, want %q", got, tt.want)
			}
		})
	}
}
