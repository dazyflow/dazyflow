// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestStore_SaveAndLoad(t *testing.T) {
	s, err := OpenFS("")
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}

	graph := core.Graph{
		ID:      "ci-pipeline",
		Version: "1",
		Nodes:   []core.Node{{ID: "a", Module: "noop"}},
	}
	commit, err := s.Save(graph, "anna@acme.com")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if commit == "" {
		t.Error("expected non-empty commit hash")
	}

	loaded, err := s.Load("ci-pipeline")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID != graph.ID || len(loaded.Nodes) != 1 {
		t.Errorf("loaded = %+v", loaded)
	}
}

func TestStore_PromoteToEnvironment(t *testing.T) {
	s, _ := OpenFS("")
	graph := core.Graph{ID: "ci-pipeline", Nodes: []core.Node{{ID: "a", Module: "noop"}}}
	commit, err := s.Save(graph, "anna@acme.com")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := s.PromoteToEnvironment("ci-pipeline", "production", commit); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	tags, err := s.Tags()
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	found := false
	for _, tag := range tags {
		if tag == "graphs/ci-pipeline/production" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected production tag, got %v", tags)
	}
}

func TestStore_ListGraphs(t *testing.T) {
	s, _ := OpenFS("")
	for _, id := range []string{"ci", "email", "etl"} {
		if _, err := s.Save(core.Graph{ID: id, Nodes: []core.Node{{ID: "x", Module: "noop"}}}, "anna"); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	ids, err := s.ListGraphs()
	if err != nil {
		t.Fatalf("ListGraphs: %v", err)
	}
	if len(ids) != 3 {
		t.Errorf("got %v, want 3", ids)
	}
}

func TestStore_HistoryAcrossSaves(t *testing.T) {
	s, _ := OpenFS("")
	g := core.Graph{ID: "g", Nodes: []core.Node{{ID: "v1", Module: "noop"}}}
	first, _ := s.Save(g, "anna")
	g.Nodes = []core.Node{{ID: "v2", Module: "noop"}}
	second, _ := s.Save(g, "anna")

	if first == second {
		t.Fatal("two saves should produce different commits")
	}

	older, err := s.LoadAt(first, "g")
	if err != nil {
		t.Fatalf("LoadAt first: %v", err)
	}
	if older.Nodes[0].ID != "v1" {
		t.Errorf("LoadAt first returned %+v", older)
	}
	newer, _ := s.LoadAt(second, "g")
	if newer.Nodes[0].ID != "v2" {
		t.Errorf("LoadAt second returned %+v", newer)
	}
}
