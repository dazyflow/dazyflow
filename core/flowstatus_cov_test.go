package core

import "testing"

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
