// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// TestTriggerEndpoints_Cov covers triggerEndpoints across all node trigger
// kinds (webhook + bearer, hosted form, cron node, poll node) and a legacy
// graph-level cron trigger.
func TestTriggerEndpoints_Cov(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)

	g := core.Graph{
		ID: "flow", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "hook", Module: webhookInputModuleID, Params: map[string]any{
				"secrets":     []string{"bearer-abc"},
				"public_form": true,
			}},
			{ID: "cronN", Module: "cron_trigger", Params: map[string]any{"cron": "0 9 * * *"}},
			{ID: "pollN", Module: "poll_trigger", Params: map[string]any{"interval_seconds": 300}},
		},
		Triggers: []core.GraphTrigger{{Type: "cron", Cron: "*/5 * * * *"}},
	}

	eps := h.gw.flowAPI().triggerEndpoints("https://app.test/", g)

	kinds := map[string]int{}
	var webhookAuth string
	for _, ep := range eps {
		kinds[ep["kind"].(string)]++
		if ep["kind"] == "webhook" {
			if a, ok := ep["auth"].(string); ok {
				webhookAuth = a
			}
			if url, _ := ep["url"].(string); url != "https://app.test/trigger/t/ws/flow" {
				t.Errorf("webhook url = %q", url)
			}
		}
	}
	if kinds["webhook"] != 1 {
		t.Errorf("webhook endpoints = %d, want 1", kinds["webhook"])
	}
	if webhookAuth != "Authorization: Bearer bearer-abc" {
		t.Errorf("webhook auth = %q", webhookAuth)
	}
	if kinds["hosted_form"] != 1 {
		t.Errorf("hosted_form endpoints = %d, want 1", kinds["hosted_form"])
	}
	if kinds["poll"] != 1 {
		t.Errorf("poll endpoints = %d, want 1", kinds["poll"])
	}
	// One cron from the node + one legacy graph-level cron.
	if kinds["cron"] != 2 {
		t.Errorf("cron endpoints = %d, want 2 (node + legacy)", kinds["cron"])
	}

	// A graph with no triggers yields an empty (non-nil) slice.
	empty := h.gw.flowAPI().triggerEndpoints("https://app.test", core.Graph{ID: "x", Tenant: "t", Workspace: "ws"})
	if empty == nil || len(empty) != 0 {
		t.Errorf("no-trigger graph = %v, want empty slice", empty)
	}
}
