// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"fmt"
	"strings"
	"testing"
)

// Every ceiling in Validate is inclusive: the documented limit is a legal
// value and only one past it is refused. Asserting only that an oversized
// graph is rejected leaves the boundary free to drift by one, which rejects
// graphs the limit says are fine.

func approverList(n int) string {
	addrs := make([]string, n)
	for i := range addrs {
		addrs[i] = fmt.Sprintf("a%d@example.test", i)
	}

	return strings.Join(addrs, ",")
}

func TestValidate_ApprovalRecipientCeilingIsInclusive(t *testing.T) {
	// Each step is capped at MaxApprovalRecipients, so the run ceiling is
	// reached with exactly that many full steps.
	build := func(steps int) Graph {
		g := Graph{}
		for i := range steps {
			g.Nodes = append(g.Nodes, Node{
				ID: fmt.Sprintf("ap%d", i), Module: ApprovalModuleID,
				Params: map[string]any{"approvers": approverList(MaxApprovalRecipients)},
			})
		}

		return g
	}
	const atCeiling = MaxGraphApprovalRecipients / MaxApprovalRecipients
	if got := GraphApprovalRecipients(build(atCeiling)); got != MaxGraphApprovalRecipients {
		t.Fatalf("precondition: %d approvers, want exactly %d", got, MaxGraphApprovalRecipients)
	}

	const msg = "approvers in one run"
	if err := Validate(build(atCeiling)); err != nil && strings.Contains(err.Error(), msg) {
		t.Errorf("a graph at exactly the approver ceiling was refused: %v", err)
	}
	if err := Validate(build(atCeiling + 1)); err == nil || !strings.Contains(err.Error(), msg) {
		t.Errorf("a graph over the approver ceiling was accepted: %v", err)
	}
}

func TestValidate_GraphByteCeilingIsInclusive(t *testing.T) {
	// ID + Module is the whole payload, so the measurement is exact.
	build := func(total int) Graph {
		return Graph{Nodes: []Node{{ID: strings.Repeat("n", total-len("text")), Module: "text"}}}
	}
	if got := ApproxGraphBytes(build(MaxGraphBytes), MaxGraphBytes); got != MaxGraphBytes {
		t.Fatalf("precondition: measured %d, want exactly %d", got, MaxGraphBytes)
	}

	const msg = "bytes of settings, labels and notes"
	if err := Validate(build(MaxGraphBytes)); err != nil && strings.Contains(err.Error(), msg) {
		t.Errorf("a graph of exactly %d bytes was refused: %v", MaxGraphBytes, err)
	}
	if err := Validate(build(MaxGraphBytes + 1)); err == nil || !strings.Contains(err.Error(), msg) {
		t.Errorf("a graph one byte over the ceiling was accepted: %v", err)
	}
}

// The per-rule report cap is inclusive too, and the "in total" summary is
// added only once the cap is actually exceeded — otherwise a report at the
// cap gains a redundant total, or one past it silently drops a line.
func TestValidate_DuplicateReportBoundary(t *testing.T) {
	build := func(dupes int) Graph {
		g := Graph{Nodes: []Node{{ID: "a", Module: "src"}, {ID: "b", Module: "sink"}}}
		for range dupes + 1 { // n+1 identical wires produce n duplicates
			g.Edges = append(g.Edges, Edge{From: "a", FromPort: "out", To: "b", ToPort: "items"})
		}

		return g
	}

	err := Validate(build(maxReportedPerRule))
	if err == nil {
		t.Fatal("duplicate wires accepted")
	}
	if got := strings.Count(err.Error(), "duplicates edge"); got != maxReportedPerRule {
		t.Errorf("named %d duplicates at the cap, want %d", got, maxReportedPerRule)
	}
	if strings.Contains(err.Error(), "duplicate connections in total") {
		t.Errorf("a summary was added at exactly the report cap: %v", err)
	}

	err = Validate(build(maxReportedPerRule + 1))
	if got := strings.Count(err.Error(), "duplicates edge"); got != maxReportedPerRule {
		t.Errorf("named %d duplicates past the cap, want it held at %d", got, maxReportedPerRule)
	}
	if want := fmt.Sprintf("%d duplicate connections in total", maxReportedPerRule+1); !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestValidate_WaypointOverrunReportBoundary(t *testing.T) {
	over := make([]Position, MaxEdgeWaypoints+1)
	build := func(edges int) Graph {
		g := Graph{Nodes: []Node{{ID: "a", Module: "src"}, {ID: "b", Module: "sink"}}}
		for i := range edges {
			// Distinct source ports so these are not ALSO duplicate wires.
			g.Edges = append(g.Edges, Edge{
				From: "a", FromPort: fmt.Sprintf("out%d", i),
				To: "b", ToPort: "items", Waypoints: over,
			})
		}

		return g
	}

	err := Validate(build(maxReportedPerRule))
	if err == nil {
		t.Fatal("over-long waypoint lists accepted")
	}
	if got := strings.Count(err.Error(), "waypoints, limit is"); got != maxReportedPerRule {
		t.Errorf("named %d overruns at the cap, want %d", got, maxReportedPerRule)
	}
	if strings.Contains(err.Error(), "waypoint limit in total") {
		t.Errorf("a summary was added at exactly the report cap: %v", err)
	}

	err = Validate(build(maxReportedPerRule + 1))
	if got := strings.Count(err.Error(), "waypoints, limit is"); got != maxReportedPerRule {
		t.Errorf("named %d overruns past the cap, want it held at %d", got, maxReportedPerRule)
	}
	want := fmt.Sprintf("%d connections exceed the %d-waypoint limit in total",
		maxReportedPerRule+1, MaxEdgeWaypoints)
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestValidate_TotalWaypointCeilingIsInclusive(t *testing.T) {
	// Spread across wires that are each within the per-edge limit, so only the
	// graph-wide total is under test.
	build := func(total int) Graph {
		g := Graph{Nodes: []Node{{ID: "a", Module: "src"}, {ID: "b", Module: "sink"}}}
		for i := 0; total > 0; i++ {
			n := min(total, MaxEdgeWaypoints)
			g.Edges = append(g.Edges, Edge{
				From: "a", FromPort: fmt.Sprintf("out%d", i),
				To: "b", ToPort: "items", Waypoints: make([]Position, n),
			})
			total -= n
		}

		return g
	}

	const msg = "connection waypoints in total"
	if err := Validate(build(MaxGraphWaypoints)); err != nil && strings.Contains(err.Error(), msg) {
		t.Errorf("a graph at exactly the waypoint ceiling was refused: %v", err)
	}
	if err := Validate(build(MaxGraphWaypoints + 1)); err == nil || !strings.Contains(err.Error(), msg) {
		t.Errorf("a graph one waypoint over the ceiling was accepted: %v", err)
	}
}

// A declared variadic minimum is inclusive: exactly Min wires satisfies it.
func TestValidateWithManifests_VariadicMinIsInclusive(t *testing.T) {
	minTwo := 2
	manifests := map[string]Manifest{
		"src":  {ID: "src", Outputs: []Port{{Port: "out"}}},
		"sink": {ID: "sink", Inputs: []Port{{Port: "items", Variadic: true, Min: &minTwo}}},
	}
	build := func(sources int) Graph {
		g := Graph{Nodes: []Node{{ID: "sink", Module: "sink"}}}
		for i := range sources {
			id := fmt.Sprintf("s%d", i)
			g.Nodes = append(g.Nodes, Node{ID: id, Module: "src"})
			g.Edges = append(g.Edges, Edge{From: id, FromPort: "out", To: "sink", ToPort: "items"})
		}

		return g
	}

	const msg = "min 2"
	if err := ValidateWithManifests(build(2), manifests); err != nil && strings.Contains(err.Error(), msg) {
		t.Errorf("exactly the declared minimum was refused: %v", err)
	}
	if err := ValidateWithManifests(build(1), manifests); err == nil || !strings.Contains(err.Error(), msg) {
		t.Errorf("one wire below the declared minimum was accepted: %v", err)
	}
}

// A wire from a switched-off step carries no data, so the port-shape checks
// skip it — but it still connects the pin. Dropping it from the fan-in tally
// makes a live step fed by a disabled upstream read as unconnected.
func TestValidateWithManifests_DisabledUpstreamStillCountsFanIn(t *testing.T) {
	minOne := 1
	manifests := map[string]Manifest{
		"src":  {ID: "src", Outputs: []Port{{Port: "out"}}},
		"sink": {ID: "sink", Inputs: []Port{{Port: "items", Variadic: true, Min: &minOne}}},
	}
	g := Graph{
		Nodes: []Node{
			{ID: "s", Module: "src", Disabled: true},
			{ID: "sink", Module: "sink"},
		},
		Edges: []Edge{{From: "s", FromPort: "out", To: "sink", ToPort: "items"}},
	}
	if err := ValidateWithManifests(g, manifests); err != nil && strings.Contains(err.Error(), "min 1") {
		t.Errorf("a disabled upstream stopped counting toward fan-in: %v", err)
	}
}
