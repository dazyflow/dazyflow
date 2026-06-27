package daemon

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestGeneratedFromGraph_Cov(t *testing.T) {
	g := core.Graph{
		Name:  "My Flow",
		Nodes: []core.Node{{ID: "a", Module: "noop", Params: map[string]any{"x": 1}}, {ID: "b", Module: "noop"}},
		Edges: []core.Edge{{From: "a", FromPort: "out", To: "b", ToPort: "in"}},
		Triggers: []core.GraphTrigger{
			{Type: "cron", Cron: "0 9 * * *"},
		},
	}
	out := generatedFromGraph(g)
	if out.Name != "My Flow" || len(out.Nodes) != 2 || len(out.Edges) != 1 {
		t.Fatalf("generated = %+v", out)
	}
	if out.Edges[0].From != "a" || out.Edges[0].ToPort != "in" {
		t.Fatalf("edge = %+v", out.Edges[0])
	}
	if out.Trigger == nil || out.Trigger.Type != "cron" || out.Trigger.Cron != "0 9 * * *" {
		t.Fatalf("trigger = %+v", out.Trigger)
	}

	// No cron trigger -> nil trigger.
	g2 := core.Graph{Name: "n", Triggers: []core.GraphTrigger{{Type: "webhook"}}}
	if out := generatedFromGraph(g2); out.Trigger != nil {
		t.Fatalf("webhook graph trigger = %+v, want nil", out.Trigger)
	}
}

func TestStampGraph_Cov(t *testing.T) {
	g := core.Graph{Nodes: []core.Node{{Module: "noop"}, {ID: "named", Module: "noop"}}}
	stampGraph(&g, "tenantX", "wsX")
	if g.Tenant != "tenantX" || g.Workspace != "wsX" {
		t.Fatalf("stamp tenant/ws = %q/%q", g.Tenant, g.Workspace)
	}
	if g.Name != "AI-generated flow" {
		t.Fatalf("default name = %q", g.Name)
	}
	if g.Nodes[0].ID != "step_1" || g.Nodes[1].ID != "named" {
		t.Fatalf("node ids = %q, %q", g.Nodes[0].ID, g.Nodes[1].ID)
	}

	// Existing name is kept.
	g2 := core.Graph{Name: "Keep Me"}
	stampGraph(&g2, "t", "w")
	if g2.Name != "Keep Me" {
		t.Fatalf("name = %q, want kept", g2.Name)
	}
}
