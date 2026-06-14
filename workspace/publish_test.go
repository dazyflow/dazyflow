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
