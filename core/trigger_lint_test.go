// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

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
			name: "cron_trigger node with valid schedule — no warning",
			graph: Graph{
				Nodes: []Node{{ID: "sched", Module: "cron_trigger", Params: map[string]any{"cron": "0 9 * * *"}}},
			},
		},
		{
			name: "cron_trigger node with garbage schedule",
			graph: Graph{
				Nodes: []Node{{ID: "sched", Module: "cron_trigger", Params: map[string]any{"cron": "not a cron"}}},
			},
			wantCode: "trigger_cron_invalid",
		},
		{
			name: "cron_trigger node with blank schedule — intentional manual, no warning",
			graph: Graph{
				Nodes: []Node{{ID: "sched", Module: "cron_trigger", Params: map[string]any{}}},
			},
		},
		{
			name: "schedule node AND graph-level cron — double-fire warning",
			graph: Graph{
				Nodes:    []Node{{ID: "sched", Module: "cron_trigger", Params: map[string]any{"cron": "0 9 * * *"}}},
				Triggers: []GraphTrigger{{Type: "cron", Cron: "0 9 * * *"}},
			},
			wantCode: "trigger_cron_duplicate_source",
		},
		{
			name: "valid poll node — no warning",
			graph: Graph{
				Nodes: []Node{{ID: "p", Module: "poll_trigger", Params: map[string]any{"interval_seconds": 300}}},
			},
		},
		{
			name: "poll node with no interval — manual-only, no warning",
			graph: Graph{
				Nodes: []Node{{ID: "p", Module: "poll_trigger"}},
			},
		},
		{
			name: "negative poll interval (node)",
			graph: Graph{
				Nodes: []Node{{ID: "p", Module: "poll_trigger", Params: map[string]any{"interval_seconds": -5}}},
			},
			wantCode: "trigger_poll_interval",
		},
		{
			name: "overflowing poll interval (node)",
			graph: Graph{
				Nodes: []Node{{ID: "p", Module: "poll_trigger", Params: map[string]any{"interval_seconds": 1 << 60}}},
			},
			wantCode: "trigger_poll_interval",
		},
		{
			name: "legacy graph-level poll is deprecated",
			graph: Graph{
				Triggers: []GraphTrigger{{Type: "poll", IntervalSeconds: 300}},
			},
			wantCode: "trigger_poll_deprecated",
		},
		{
			name: "webhook node with secret — no warning",
			graph: Graph{
				Nodes: []Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s3cr3t"}}}},
			},
		},
		{
			name: "webhook node with neither secret nor public form",
			graph: Graph{
				Nodes: []Node{webhookInputNode()},
			},
			wantCode: "trigger_webhook_no_secret",
		},
		{
			name: "webhook node with public form, no secret — fine (form is secret-less)",
			graph: Graph{
				Nodes: []Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"public_form": true}}},
			},
		},
		{
			name: "legacy graph-level webhook is deprecated",
			graph: Graph{
				Nodes:    []Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}}}},
				Triggers: []GraphTrigger{{Type: "webhook", Secret: "s"}},
			},
			wantCode: "trigger_webhook_deprecated",
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
