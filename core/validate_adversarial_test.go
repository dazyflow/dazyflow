// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"errors"
	"testing"
	"time"
)

// withinDeadline runs fn and fails if it doesn't finish in budget — a guard
// that the validator/topsort can't be driven into a hang by graph size or
// shape (both are iterative, so this should never trip; the test pins it).
func withinDeadline(t *testing.T, budget time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(budget):
		t.Fatalf("validation did not finish within %s — possible hang", budget)
	}
}

// TestValidate_HugeLinearChainIsFast proves a 100k-node linear graph validates
// (no cycle) without recursion blowup or pathological slowdown.
func TestValidate_HugeLinearChainIsFast(t *testing.T) {
	const n = 100000
	g := Graph{Nodes: make([]Node, n), Edges: make([]Edge, 0, n-1)}
	for i := 0; i < n; i++ {
		g.Nodes[i] = Node{ID: "n" + itoa(i), Module: "noop"}
		if i > 0 {
			g.Edges = append(g.Edges, Edge{
				From: "n" + itoa(i-1), FromPort: "out", To: "n" + itoa(i), ToPort: "in",
			})
		}
	}
	withinDeadline(t, 10*time.Second, func() {
		if _, err := TopologicalOrder(g); err != nil {
			t.Errorf("TopologicalOrder on a DAG returned %v", err)
		}
	})
}

// TestValidate_HugeCycleDetectedNotHung proves a 100k-node ring is reported as
// a cycle without stack overflow or hang.
func TestValidate_HugeCycleDetectedNotHung(t *testing.T) {
	const n = 100000
	g := Graph{Nodes: make([]Node, n), Edges: make([]Edge, n)}
	for i := 0; i < n; i++ {
		g.Nodes[i] = Node{ID: "n" + itoa(i), Module: "noop"}
		g.Edges[i] = Edge{
			From: "n" + itoa(i), FromPort: "out",
			To: "n" + itoa((i+1)%n), ToPort: "in",
		}
	}
	withinDeadline(t, 10*time.Second, func() {
		if _, err := TopologicalOrder(g); !errors.Is(err, ErrCycle) {
			t.Errorf("ring graph: err = %v, want ErrCycle", err)
		}
		if err := Validate(g); err == nil {
			t.Error("Validate accepted a fully-cyclic graph")
		}
	})
}

// TestValidate_MalformedGraphsNeverPanic throws structurally broken graphs at
// Validate. Each must return an error (never accept the graph) and must never
// panic.
func TestValidate_MalformedGraphsNeverPanic(t *testing.T) {
	cases := map[string]Graph{
		"empty node ID":    {Nodes: []Node{{ID: "", Module: "x"}}},
		"duplicate IDs":    {Nodes: []Node{{ID: "a", Module: "x"}, {ID: "a", Module: "y"}}},
		"empty module":     {Nodes: []Node{{ID: "a", Module: ""}}},
		"self loop":        {Nodes: []Node{{ID: "a", Module: "x"}}, Edges: []Edge{{From: "a", To: "a", FromPort: "out", ToPort: "in"}}},
		"edge unknown src": {Nodes: []Node{{ID: "a", Module: "x"}}, Edges: []Edge{{From: "ghost", To: "a", FromPort: "out", ToPort: "in"}}},
		"edge empty ports": {Nodes: []Node{{ID: "a", Module: "x"}, {ID: "b", Module: "y"}}, Edges: []Edge{{From: "a", To: "b"}}},
	}
	for name, g := range cases {
		g := g
		t.Run(name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("Validate panicked: %v", p)
				}
			}()
			if err := Validate(g); err == nil {
				t.Errorf("Validate accepted a malformed graph (%s)", name)
			}
		})
	}
}

// TestValidateWithManifests_AdversarialPorts exercises the manifest-aware
// rules — unknown module/port, MIME mismatch, missing required input, and
// over-connected non-variadic input — proving each is rejected without a panic.
func TestValidateWithManifests_AdversarialPorts(t *testing.T) {
	manifests := map[string]Manifest{
		"src": {ID: "src", Outputs: []Port{{Port: "out", MIME: []string{"application/json"}}}},
		"txt": {ID: "txt", Outputs: []Port{{Port: "out", MIME: []string{"text/plain"}}}},
		"sink": {ID: "sink", Inputs: []Port{
			{Port: "need", MIME: []string{"application/json"}, Required: true},
			{Port: "one", MIME: []string{"application/json"}},
		}},
	}

	cases := map[string]Graph{
		"unknown module": {
			Nodes: []Node{{ID: "a", Module: "does-not-exist"}},
		},
		"unknown from-port": {
			Nodes: []Node{{ID: "a", Module: "src"}, {ID: "b", Module: "sink"}},
			Edges: []Edge{{From: "a", FromPort: "ghost", To: "b", ToPort: "need"}},
		},
		"unknown to-port": {
			Nodes: []Node{{ID: "a", Module: "src"}, {ID: "b", Module: "sink"}},
			Edges: []Edge{{From: "a", FromPort: "out", To: "b", ToPort: "ghost"}},
		},
		"mime mismatch": {
			Nodes: []Node{{ID: "a", Module: "txt"}, {ID: "b", Module: "sink"}},
			Edges: []Edge{{From: "a", FromPort: "out", To: "b", ToPort: "need"}},
		},
		"required unconnected": {
			Nodes: []Node{{ID: "b", Module: "sink"}},
		},
		"non-variadic fan-in": {
			Nodes: []Node{{ID: "a", Module: "src"}, {ID: "a2", Module: "src"}, {ID: "b", Module: "sink"}},
			Edges: []Edge{
				{From: "a", FromPort: "out", To: "b", ToPort: "need"},
				{From: "a2", FromPort: "out", To: "b", ToPort: "need"},
			},
		},
	}
	for name, g := range cases {
		g := g
		t.Run(name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("ValidateWithManifests panicked: %v", p)
				}
			}()
			if err := ValidateWithManifests(g, manifests); err == nil {
				t.Errorf("accepted an invalid graph (%s)", name)
			}
		})
	}
}

// itoa avoids strconv import churn and keeps the huge-graph builders allocation-light.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}
