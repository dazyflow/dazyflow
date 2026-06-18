package workspace

import (
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

// TestStore_PublishAndLoadPublished covers the publish-gating primitives:
// before publish, LoadPublishedOrHead falls back to HEAD (existing flows
// keep firing); after publish, it pins to the published revision even when
// HEAD has moved on (a draft edit doesn't go live until republished);
// re-publishing an older commit is a rollback.
func TestStore_PublishAndLoadPublished(t *testing.T) {
	s, err := OpenFS("")
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	mk := func(node string) core.Graph {
		return core.Graph{ID: "flow1", Nodes: []core.Node{{ID: node, Module: "noop"}}}
	}

	v1, err := s.Save(mk("a"), "anna@acme.com")
	if err != nil {
		t.Fatalf("save v1: %v", err)
	}

	// Never published yet: PublishedCommit is empty (not an error), and
	// LoadPublishedOrHead falls back to HEAD.
	if pc, err := s.PublishedCommit("flow1"); err != nil || pc != "" {
		t.Fatalf("PublishedCommit before publish = (%q, %v), want (\"\", nil)", pc, err)
	}
	g, err := s.LoadPublishedOrHead("flow1")
	if err != nil {
		t.Fatalf("LoadPublishedOrHead (fallback): %v", err)
	}
	if g.Nodes[0].ID != "a" {
		t.Fatalf("fallback loaded node %q, want HEAD node \"a\"", g.Nodes[0].ID)
	}

	// Publish v1, then move HEAD forward to a draft (node "b").
	if err := s.PromoteToEnvironment("flow1", PublishedEnv, v1); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := s.Save(mk("b"), "anna@acme.com"); err != nil {
		t.Fatalf("save v2 (draft): %v", err)
	}

	// LoadPublishedOrHead must still return the PUBLISHED v1 (node "a"),
	// not the draft HEAD (node "b") — the core gating guarantee.
	pub, err := s.LoadPublishedOrHead("flow1")
	if err != nil {
		t.Fatalf("LoadPublishedOrHead (published): %v", err)
	}
	if pub.Nodes[0].ID != "a" {
		t.Fatalf("published load node %q, want \"a\" (draft must not go live)", pub.Nodes[0].ID)
	}
	// Plain Load (the draft path) sees the new HEAD.
	head, err := s.Load("flow1")
	if err != nil {
		t.Fatalf("Load HEAD: %v", err)
	}
	if head.Nodes[0].ID != "b" {
		t.Fatalf("HEAD load node %q, want draft \"b\"", head.Nodes[0].ID)
	}

	// Publish the new draft → live advances to node "b".
	headCommit, err := s.PublishedCommit("flow1")
	if err != nil || headCommit != v1 {
		t.Fatalf("PublishedCommit = (%q, %v), want (%q, nil)", headCommit, err, v1)
	}
	if err := s.PromoteToEnvironment("flow1", PublishedEnv, "HEAD"); err != nil {
		t.Fatalf("publish HEAD: %v", err)
	}
	pub2, err := s.LoadPublishedOrHead("flow1")
	if err != nil {
		t.Fatalf("LoadPublishedOrHead after republish: %v", err)
	}
	if pub2.Nodes[0].ID != "b" {
		t.Fatalf("after republish, live node %q, want \"b\"", pub2.Nodes[0].ID)
	}

	// Rollback: re-publish the original v1 commit → live returns to "a".
	if err := s.PromoteToEnvironment("flow1", PublishedEnv, v1); err != nil {
		t.Fatalf("rollback to v1: %v", err)
	}
	rolled, err := s.LoadPublishedOrHead("flow1")
	if err != nil {
		t.Fatalf("LoadPublishedOrHead after rollback: %v", err)
	}
	if rolled.Nodes[0].ID != "a" {
		t.Fatalf("after rollback, live node %q, want \"a\"", rolled.Nodes[0].ID)
	}
}

// TestStore_RevisionLabel covers the per-commit label model: labels round-trip
// through SetRevisionLabel/RevisionLabel, surface in History keyed to their
// commit, persist across re-publishes, and are replaceable (including clear).
func TestStore_RevisionLabel(t *testing.T) {
	s, err := OpenFS("")
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	mk := func(node string) core.Graph {
		return core.Graph{ID: "flow1", Nodes: []core.Node{{ID: node, Module: "noop"}}}
	}

	v1, err := s.Save(mk("a"), "anna@acme.com")
	if err != nil {
		t.Fatalf("save v1: %v", err)
	}
	v2, err := s.Save(mk("b"), "anna@acme.com")
	if err != nil {
		t.Fatalf("save v2: %v", err)
	}

	// Unlabeled commit reads back empty (absent tag is not an error).
	if lbl, err := s.RevisionLabel("flow1", v1); err != nil || lbl != "" {
		t.Fatalf("RevisionLabel unlabeled = (%q, %v), want (\"\", nil)", lbl, err)
	}

	// Label v1, then move HEAD past it. The label stays keyed to v1.
	if err := s.SetRevisionLabel("flow1", v1, "Black Friday config"); err != nil {
		t.Fatalf("SetRevisionLabel v1: %v", err)
	}
	if lbl, err := s.RevisionLabel("flow1", v1); err != nil || lbl != "Black Friday config" {
		t.Fatalf("RevisionLabel v1 = (%q, %v), want (\"Black Friday config\", nil)", lbl, err)
	}
	if lbl, _ := s.RevisionLabel("flow1", v2); lbl != "" {
		t.Fatalf("RevisionLabel v2 = %q, want \"\" (label is per-commit)", lbl)
	}

	// History carries each revision's label on the right commit.
	revs, err := s.History("flow1", 100)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	for _, r := range revs {
		switch r.Commit {
		case v1:
			if r.Label != "Black Friday config" {
				t.Fatalf("history v1 label = %q, want \"Black Friday config\"", r.Label)
			}
		case v2:
			if r.Label != "" {
				t.Fatalf("history v2 label = %q, want \"\"", r.Label)
			}
		}
	}

	// Re-labeling replaces; empty clears.
	if err := s.SetRevisionLabel("flow1", v1, "pre-GDPR"); err != nil {
		t.Fatalf("relabel v1: %v", err)
	}
	if lbl, _ := s.RevisionLabel("flow1", v1); lbl != "pre-GDPR" {
		t.Fatalf("after relabel, v1 = %q, want \"pre-GDPR\"", lbl)
	}
	if err := s.SetRevisionLabel("flow1", v1, ""); err != nil {
		t.Fatalf("clear v1 label: %v", err)
	}
	if lbl, _ := s.RevisionLabel("flow1", v1); lbl != "" {
		t.Fatalf("after clear, v1 = %q, want \"\"", lbl)
	}
}

// TestStore_ClearEnvironment covers unpublishing: clearing the published tag
// drops PublishedCommit back to "" (so the scheduler treats the flow as not
// live) while LoadPublishedOrHead falls back to the current HEAD draft. It's
// idempotent — clearing a never-published env is a no-op, not an error.
func TestStore_ClearEnvironment(t *testing.T) {
	s, err := OpenFS("")
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	mk := func(node string) core.Graph {
		return core.Graph{ID: "flow1", Nodes: []core.Node{{ID: node, Module: "noop"}}}
	}

	// Clearing before anything is published is a no-op success.
	if err := s.ClearEnvironment("flow1", PublishedEnv); err != nil {
		t.Fatalf("ClearEnvironment (never published) = %v, want nil", err)
	}

	v1, err := s.Save(mk("a"), "anna@acme.com")
	if err != nil {
		t.Fatalf("save v1: %v", err)
	}
	if err := s.PromoteToEnvironment("flow1", PublishedEnv, v1); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := s.Save(mk("b"), "anna@acme.com"); err != nil {
		t.Fatalf("save v2 (draft): %v", err)
	}
	if pc, err := s.PublishedCommit("flow1"); err != nil || pc != v1 {
		t.Fatalf("PublishedCommit after publish = (%q, %v), want (%q, nil)", pc, err, v1)
	}

	// Unpublish: the tag is gone, PublishedCommit is empty again.
	if err := s.ClearEnvironment("flow1", PublishedEnv); err != nil {
		t.Fatalf("ClearEnvironment: %v", err)
	}
	if pc, err := s.PublishedCommit("flow1"); err != nil || pc != "" {
		t.Fatalf("PublishedCommit after unpublish = (%q, %v), want (\"\", nil)", pc, err)
	}
	// With nothing published, LoadPublishedOrHead falls back to HEAD (node "b").
	g, err := s.LoadPublishedOrHead("flow1")
	if err != nil {
		t.Fatalf("LoadPublishedOrHead after unpublish: %v", err)
	}
	if g.Nodes[0].ID != "b" {
		t.Fatalf("after unpublish, fallback node %q, want HEAD \"b\"", g.Nodes[0].ID)
	}

	// Idempotent: clearing again still succeeds.
	if err := s.ClearEnvironment("flow1", PublishedEnv); err != nil {
		t.Fatalf("ClearEnvironment (second clear) = %v, want nil", err)
	}

	// An empty env name is rejected (mirrors PromoteToEnvironment).
	if err := s.ClearEnvironment("flow1", ""); err == nil {
		t.Fatal("ClearEnvironment with empty env = nil, want error")
	}
}
