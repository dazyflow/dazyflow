package core

import "testing"

// hasLintCode reports whether any issue carries the given code.
func hasLintCode(issues []LintIssue, code string) bool {
	for _, i := range issues {
		if i.Code == code {
			return true
		}
	}
	return false
}

// webhookInputNode is the sink a hosted form delivers to; several cases
// need one present (or absent) to exercise trigger_form_no_sink.
func webhookInputNode() Node { return Node{ID: "in", Module: "webhook_input"} }

func TestLintTriggers_FlagsBadConfigs(t *testing.T) {
	cases := []struct {
		name     string
		graph    Graph
		wantCode string // "" → expect NO trigger lint at all
	}{
		{
			name: "valid cron — no warning",
			graph: Graph{
				Nodes:    []Node{{ID: "a", Module: "delay"}},
				Triggers: []GraphTrigger{{Type: "cron", Cron: "0 9 * * *"}},
			},
		},
		{
			name: "empty cron",
			graph: Graph{
				Triggers: []GraphTrigger{{Type: "cron", Cron: ""}},
			},
			wantCode: "trigger_cron_invalid",
		},
		{
			name: "garbage cron",
			graph: Graph{
				Triggers: []GraphTrigger{{Type: "cron", Cron: "not a cron"}},
			},
			wantCode: "trigger_cron_invalid",
		},
		{
			name: "impossible cron date (Feb 30)",
			graph: Graph{
				Triggers: []GraphTrigger{{Type: "cron", Cron: "0 0 30 2 *"}},
			},
			wantCode: "trigger_cron_never_fires",
		},
		{
			name: "valid poll — no warning",
			graph: Graph{
				Triggers: []GraphTrigger{{Type: "poll", IntervalSeconds: 300}},
			},
		},
		{
			name: "zero poll interval",
			graph: Graph{
				Triggers: []GraphTrigger{{Type: "poll", IntervalSeconds: 0}},
			},
			wantCode: "trigger_poll_interval",
		},
		{
			name: "negative poll interval",
			graph: Graph{
				Triggers: []GraphTrigger{{Type: "poll", IntervalSeconds: -5}},
			},
			wantCode: "trigger_poll_interval",
		},
		{
			name: "overflowing poll interval",
			graph: Graph{
				Triggers: []GraphTrigger{{Type: "poll", IntervalSeconds: 1 << 60}},
			},
			wantCode: "trigger_poll_interval",
		},
		{
			name: "webhook with secret — no warning",
			graph: Graph{
				Nodes:    []Node{webhookInputNode()},
				Triggers: []GraphTrigger{{Type: "webhook", Secret: "s3cr3t"}},
			},
		},
		{
			name: "webhook without secret and no form",
			graph: Graph{
				Nodes:    []Node{webhookInputNode()},
				Triggers: []GraphTrigger{{Type: "webhook", Secret: ""}},
			},
			wantCode: "trigger_webhook_no_secret",
		},
		{
			name: "public form without secret is fine (form is secret-less)",
			graph: Graph{
				Nodes:    []Node{webhookInputNode()},
				Triggers: []GraphTrigger{{Type: "webhook", Secret: "", PublicForm: true}},
			},
		},
		{
			name: "public form with no webhook_input node",
			graph: Graph{
				Nodes:    []Node{{ID: "x", Module: "delay"}},
				Triggers: []GraphTrigger{{Type: "webhook", Secret: "s", PublicForm: true}},
			},
			wantCode: "trigger_form_no_sink",
		},
		{
			name: "unknown trigger type",
			graph: Graph{
				Triggers: []GraphTrigger{{Type: "carrier-pigeon"}},
			},
			wantCode: "trigger_unknown_type",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := lintTriggers(tc.graph)
			if tc.wantCode == "" {
				if len(issues) != 0 {
					t.Errorf("expected no trigger lint, got %+v", issues)
				}
				return
			}
			if !hasLintCode(issues, tc.wantCode) {
				t.Errorf("expected lint code %q, got %+v", tc.wantCode, issues)
			}
			for _, i := range issues {
				if i.Severity != LintWarn {
					t.Errorf("trigger lint %q severity = %q, want warn", i.Code, i.Severity)
				}
			}
		})
	}
}

// TestLintTriggers_WiredThroughLintGraph confirms the rule is actually
// reachable via the public LintGraph entry point (not just the private
// helper), since that's what the save path calls.
func TestLintTriggers_WiredThroughLintGraph(t *testing.T) {
	g := Graph{
		Nodes:    []Node{{ID: "a", Module: "delay"}},
		Triggers: []GraphTrigger{{Type: "cron", Cron: "0 0 30 2 *"}},
	}
	if !hasLintCode(LintGraph(g), "trigger_cron_never_fires") {
		t.Error("LintGraph did not surface the impossible-cron trigger warning")
	}
}
