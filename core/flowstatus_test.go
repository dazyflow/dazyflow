package core

import "testing"

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
