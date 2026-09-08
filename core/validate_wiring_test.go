// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strconv"
	"strings"
	"testing"
)

func wiringManifests() map[string]Manifest {
	return map[string]Manifest{
		"src": {ID: "src", Outputs: []Port{{Port: "out"}}},
		"variadic": {ID: "variadic", Inputs: []Port{
			{Port: "items", Variadic: true},
		}},
		"dyn": {
			ID:           "dyn",
			DynamicPorts: true,
			Inputs:       []Port{{Port: "in"}},
			Outputs:      []Port{{Port: "out"}},
		},
	}
}

// A wire drawn twice between the same ports is refused wherever it lands — a
// variadic pin used to accept any number of them.
func TestValidate_RefusesDuplicateEdges(t *testing.T) {
	g := Graph{
		Nodes: []Node{{ID: "a", Module: "src"}, {ID: "b", Module: "variadic"}},
		Edges: []Edge{
			{From: "a", FromPort: "out", To: "b", ToPort: "items"},
			{From: "a", FromPort: "out", To: "b", ToPort: "items"},
		},
	}
	err := Validate(g)
	if err == nil || !strings.Contains(err.Error(), "duplicates edge 0") {
		t.Fatalf("duplicate wire: err = %v, want a duplicate-edge error", err)
	}
	g.Edges[1].Waypoints = []Position{{X: 1, Y: 1}}
	if err := Validate(g); err == nil {
		t.Error("a duplicate wire with different waypoints was accepted")
	}
}

func TestValidate_DuplicateReportIsBounded(t *testing.T) {
	const n = 500
	g := Graph{Nodes: []Node{{ID: "a", Module: "src"}, {ID: "b", Module: "variadic"}}}
	for i := 0; i < n; i++ {
		g.Edges = append(g.Edges, Edge{From: "a", FromPort: "out", To: "b", ToPort: "items"})
	}
	err := Validate(g)
	if err == nil {
		t.Fatal("duplicates accepted")
	}
	if lines := strings.Count(err.Error(), "\n") + 1; lines > maxReportedPerRule+4 {
		t.Errorf("%d duplicate wires produced %d error lines, want it collapsed to a count", n, lines)
	}
	if !strings.Contains(err.Error(), "duplicate connections in total") {
		t.Errorf("no total reported: %v", err)
	}
}

// A variadic port with no Max of its own gets the default ceiling; nothing
// but the graph's connection cap used to bound it.
func TestValidateRuntime_VariadicFanInDefaultMax(t *testing.T) {
	build := func(n int) Graph {
		g := Graph{Nodes: []Node{{ID: "sink", Module: "variadic"}}}
		for i := 0; i < n; i++ {
			id := "s" + string(rune('A'+i%26)) + string(rune('a'+i/26))
			g.Nodes = append(g.Nodes, Node{ID: id, Module: "src"})
			g.Edges = append(g.Edges, Edge{From: id, FromPort: "out", To: "sink", ToPort: "items"})
		}
		return g
	}
	if err := ValidateRuntime(build(DefaultMaxVariadicFanIn), wiringManifests()); err != nil {
		t.Errorf("fan-in at the ceiling was refused: %v", err)
	}
	err := ValidateRuntime(build(DefaultMaxVariadicFanIn+1), wiringManifests())
	if err == nil || !strings.Contains(err.Error(), "max 64") {
		t.Errorf("fan-in past the ceiling: err = %v, want a max-connections error", err)
	}

	m := wiringManifests()
	two := 2
	m["variadic"] = Manifest{ID: "variadic", Inputs: []Port{{Port: "items", Variadic: true, Max: &two}}}
	if err := ValidateRuntime(build(3), m); err == nil {
		t.Error("a port's own Max was not applied")
	}
}

func TestValidateRuntime_ManifestMaxIsClamped(t *testing.T) {
	build := func(n int) Graph {
		g := Graph{Nodes: []Node{{ID: "sink", Module: "variadic"}}}
		for i := 0; i < n; i++ {
			id := "s" + strconv.Itoa(i)
			g.Nodes = append(g.Nodes, Node{ID: id, Module: "src"})
			g.Edges = append(g.Edges, Edge{From: id, FromPort: "out", To: "sink", ToPort: "items"})
		}
		return g
	}
	m := wiringManifests()
	huge := 1_000_000
	m["variadic"] = Manifest{ID: "variadic", Inputs: []Port{{Port: "items", Variadic: true, Max: &huge}}}

	if err := ValidateRuntime(build(DefaultMaxVariadicFanIn+1), m); err != nil {
		t.Errorf("a declared max above the default was refused: %v", err)
	}
	if err := ValidateRuntime(build(MaxVariadicFanIn), m); err != nil {
		t.Errorf("fan-in at the absolute ceiling was refused: %v", err)
	}
	err := ValidateRuntime(build(MaxVariadicFanIn+1), m)
	if err == nil || !strings.Contains(err.Error(), "max 1024") {
		t.Errorf("a manifest raised its own fan-in ceiling past %d: err = %v", MaxVariadicFanIn, err)
	}
}

func TestValidateRuntime_DynamicPortsFanIn(t *testing.T) {
	g := Graph{
		Nodes: []Node{
			{ID: "call", Module: "dyn"},
			{ID: "a", Module: "src"},
			{ID: "b", Module: "src"},
		},
		Edges: []Edge{
			{From: "a", FromPort: "out", To: "call", ToPort: "anything"},
			{From: "b", FromPort: "out", To: "call", ToPort: "anything"},
		},
	}
	err := ValidateRuntime(g, wiringManifests())
	if err == nil || !strings.Contains(err.Error(), "has 2 connections") {
		t.Fatalf("two wires into one dynamic port: err = %v, want a fan-in error", err)
	}

	g.Edges[1].ToPort = "other"
	if err := ValidateRuntime(g, wiringManifests()); err != nil {
		t.Errorf("one wire per dynamic port was refused: %v", err)
	}
}

// Editor-only metadata is bounded: it rides in every run record and is
// re-parsed on every dispatch pass, for bytes the engine never reads.
func TestValidate_EditorMetadataCaps(t *testing.T) {
	g := Graph{
		Nodes: []Node{{ID: "a", Module: "src"}, {ID: "b", Module: "variadic"}},
		Edges: []Edge{{From: "a", FromPort: "out", To: "b", ToPort: "items"}},
	}
	if err := Validate(g); err != nil {
		t.Fatalf("baseline graph rejected: %v", err)
	}

	g.Edges[0].Waypoints = make([]Position, MaxEdgeWaypoints+1)
	if err := Validate(g); err == nil || !strings.Contains(err.Error(), "waypoints") {
		t.Errorf("waypoints past the cap: err = %v", err)
	}
	g.Edges[0].Waypoints = make([]Position, MaxEdgeWaypoints)
	if err := Validate(g); err != nil {
		t.Errorf("waypoints at the cap were refused: %v", err)
	}

	g.Frames = make([]Frame, MaxGraphFrames+1)
	if err := Validate(g); err == nil || !strings.Contains(err.Error(), "frames") {
		t.Errorf("frames past the cap: err = %v", err)
	}
	g.Frames = make([]Frame, MaxGraphFrames)
	if err := Validate(g); err != nil {
		t.Errorf("frames at the cap were refused: %v", err)
	}
}

func TestValidateRuntime_UnknownModuleFanIn(t *testing.T) {
	g := Graph{
		Nodes: []Node{
			{ID: "call", Module: "runner.mystery_step"},
			{ID: "a", Module: "src"},
			{ID: "b", Module: "src"},
		},
		Edges: []Edge{
			{From: "a", FromPort: "out", To: "call", ToPort: "in"},
			{From: "b", FromPort: "out", To: "call", ToPort: "in"},
		},
	}
	err := ValidateRuntime(g, wiringManifests())
	if err == nil || !strings.Contains(err.Error(), "has 2 connections") {
		t.Fatalf("two wires into one input of a catalog-less step: err = %v, want a fan-in error", err)
	}

	g.Edges[1].ToPort = "other"
	if err := ValidateRuntime(g, wiringManifests()); err != nil {
		t.Errorf("one wire per port on a catalog-less step was refused: %v", err)
	}

	g.Edges[1].ToPort = "in"
	g.Nodes[0].Disabled = true
	if err := ValidateRuntime(g, wiringManifests()); err != nil {
		t.Errorf("fan-in flagged on a switched-off step: %v", err)
	}
}

func TestValidate_GraphScaleCaps(t *testing.T) {
	base := func() Graph {
		return Graph{
			Nodes: []Node{{ID: "a", Module: "src"}, {ID: "b", Module: "variadic"}},
			Edges: []Edge{{From: "a", FromPort: "out", To: "b", ToPort: "items"}},
		}
	}

	g := base()
	for range MaxGraphTriggers {
		g.Triggers = append(g.Triggers, GraphTrigger{Type: "cron", Cron: "* * * * *"})
	}
	if err := Validate(g); err != nil {
		t.Errorf("triggers at the cap were refused: %v", err)
	}
	g.Triggers = append(g.Triggers, GraphTrigger{Type: "cron", Cron: "* * * * *"})
	if err := Validate(g); err == nil || !strings.Contains(err.Error(), "triggers") {
		t.Errorf("triggers past the cap: err = %v", err)
	}

	g = base()
	perEdge := make([]Position, MaxEdgeWaypoints)
	for total := 0; total <= MaxGraphWaypoints; total += MaxEdgeWaypoints {
		g.Nodes = append(g.Nodes, Node{ID: "n" + strconv.Itoa(len(g.Nodes)), Module: "src"})
		g.Edges = append(g.Edges, Edge{
			From: g.Nodes[len(g.Nodes)-1].ID, FromPort: "out",
			To: "b", ToPort: "items", Waypoints: perEdge,
		})
	}
	err := Validate(g)
	if err == nil || !strings.Contains(err.Error(), "waypoints in total") {
		t.Errorf("waypoints past the graph total: err = %v", err)
	}

	// Bytes: one big string counted once per node it appears on, so the test
	// allocates a megabyte rather than the ceiling.
	g = base()
	big := strings.Repeat("x", 1<<20)
	for len(g.Nodes) < (MaxGraphBytes>>20)+2 {
		g.Nodes = append(g.Nodes, Node{
			ID: "n" + strconv.Itoa(len(g.Nodes)), Module: "src",
			Params: map[string]any{"note": big},
		})
	}
	if err := Validate(g); err == nil || !strings.Contains(err.Error(), "bytes of settings") {
		t.Errorf("a graph past the byte ceiling: err = %v", err)
	}

	deep := any("x")
	for range 200 {
		deep = []any{deep}
	}
	g = base()
	g.Nodes[0].Params = map[string]any{"junk": deep}
	if err := Validate(g); err == nil || !strings.Contains(err.Error(), "bytes of settings") {
		t.Errorf("a setting nested past the walk's depth cap: err = %v", err)
	}
}

// A trigger STEP produces a scheduler entry exactly as a declared trigger
// does, and the scheduler keys a step's entry by node ID — identical schedules
// on steps cannot collapse into one the way array entries do. Capping only the
// array left the flood reachable by pasting the step: 200 copies of
// "* * * * *" were 200 entries. Both count against MaxGraphTriggers now, and
// against the SAME budget, so splitting the flood across the two buys nothing.
func TestValidateRuntime_TriggerStepsShareTheTriggerCap(t *testing.T) {
	mans := wiringManifests()
	mans["sched"] = Manifest{ID: "sched", ExecutionModel: ExecutionTrigger,
		Outputs: []Port{{Port: "out"}}}

	steps := func(n int) Graph {
		g := Graph{}
		for i := range n {
			g.Nodes = append(g.Nodes, Node{ID: "t" + strconv.Itoa(i), Module: "sched"})
		}
		return g
	}

	if err := ValidateRuntime(steps(MaxGraphTriggers), mans); err != nil {
		t.Errorf("trigger steps at the cap were refused: %v", err)
	}
	err := ValidateRuntime(steps(MaxGraphTriggers+1), mans)
	if err == nil || !strings.Contains(err.Error(), "triggers") {
		t.Errorf("trigger steps past the cap: err = %v", err)
	}

	split := steps(MaxGraphTriggers / 2)
	for range MaxGraphTriggers/2 + 1 {
		split.Triggers = append(split.Triggers, GraphTrigger{Type: "cron", Cron: "* * * * *"})
	}
	if err := ValidateRuntime(split, mans); err == nil || !strings.Contains(err.Error(), "triggers") {
		t.Errorf("a flood split across steps and the array: err = %v", err)
	}

	plain := Graph{}
	for i := range MaxGraphTriggers * 4 {
		plain.Nodes = append(plain.Nodes, Node{ID: "n" + strconv.Itoa(i), Module: "src"})
	}
	if err := ValidateRuntime(plain, mans); err != nil {
		t.Errorf("ordinary steps counted as triggers: %v", err)
	}
}

func TestValidate_ApprovalRecipientsShareOneBudget(t *testing.T) {
	gate := func(id string, n int) Node {
		var list []string
		for i := 0; i < n; i++ {
			list = append(list, "a"+id+strconv.Itoa(i)+"@example.com")
		}
		return Node{ID: id, Module: ApprovalModuleID,
			Params: map[string]any{"approvers": strings.Join(list, ",")}}
	}
	build := func(gates, each int) Graph {
		g := Graph{}
		for i := 0; i < gates; i++ {
			g.Nodes = append(g.Nodes, gate("g"+strconv.Itoa(i), each))
		}
		return g
	}

	if err := Validate(build(3, 5)); err != nil {
		t.Errorf("an ordinary approval flow was refused: %v", err)
	}
	// One step cannot carry more than the per-step cap toward the budget, so a
	// single gate can never trip the graph rule on its own.
	if err := Validate(build(1, MaxApprovalRecipients*100)); err != nil {
		t.Errorf("one over-long list should be capped at read, not refused here: %v", err)
	}
	err := Validate(build(40, MaxApprovalRecipients))
	if err == nil || !strings.Contains(err.Error(), "approvers in one run") {
		t.Errorf("40 gates x %d approvers: err = %v, want the run budget to bind",
			MaxApprovalRecipients, err)
	}
	// A switched-off gate never parks, so it never mails.
	off := build(40, MaxApprovalRecipients)
	for i := range off.Nodes {
		off.Nodes[i].Disabled = true
	}
	if err := Validate(off); err != nil {
		t.Errorf("disabled approval steps counted toward the budget: %v", err)
	}
}
