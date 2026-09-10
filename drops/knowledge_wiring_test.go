// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package drops_test

import (
	"slices"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/llm"
)

// The Knowledge connection offers a fixed list of providers, written by hand
// because package init order does not promise the providers have registered
// by the time the manifest is built. This is what keeps the hand-written list
// honest: with every drop loaded, the dropdown must be exactly the providers
// that can actually embed — no missing one, and none offered that would fail
// the moment someone picked it.
func TestKnowledgeConnectionOffersEveryEmbeddingProvider(t *testing.T) {
	want := llm.EmbeddingIntegrations()
	if len(want) == 0 {
		t.Fatal("no provider registered an Embedder — the Knowledge steps cannot work")
	}
	slices.Sort(want)

	for _, id := range []string{"knowledge_add", "knowledge_search"} {
		tr, ok := engine.Default.Get(id)
		if !ok {
			t.Fatalf("%s is not registered", id)
		}
		var options []string
		for _, f := range tr.Manifest().ConnectionFields {
			if f.Key == "provider" {
				options = slices.Clone(f.Options)
			}
		}
		if options == nil {
			t.Fatalf("%s has no 'provider' connection field", id)
		}
		slices.Sort(options)
		if !slices.Equal(options, want) {
			t.Errorf("%s offers %v, but the providers that can embed are %v", id, options, want)
		}
	}
}

// Anthropic sells no embeddings API, so Claude must not appear in a dropdown
// that would fail the moment someone picked it.
func TestClaudeIsNotOfferedForEmbeddings(t *testing.T) {
	if _, ok := llm.EmbedderByIntegration("Claude"); ok {
		t.Error("Claude is offered as an embedding provider — Anthropic has no embeddings API")
	}
}

// Both halves read the same connection, or a base written by one could not be
// searched by the other.
func TestKnowledgeStepsShareOneConnection(t *testing.T) {
	fields := map[string][]core.ConnectionField{}
	for _, id := range []string{"knowledge_add", "knowledge_search"} {
		tr, ok := engine.Default.Get(id)
		if !ok {
			t.Fatalf("%s is not registered", id)
		}
		m := tr.Manifest()
		if m.Integration != "Knowledge" {
			t.Errorf("%s is integration %q, want Knowledge — connection injection keys off this", id, m.Integration)
		}
		fields[id] = m.ConnectionFields
	}
	if len(fields["knowledge_add"]) != len(fields["knowledge_search"]) {
		t.Fatalf("the two steps declare different connection fields: %v vs %v",
			fields["knowledge_add"], fields["knowledge_search"])
	}
	for i, f := range fields["knowledge_add"] {
		if other := fields["knowledge_search"][i]; other.Key != f.Key || other.Secret != f.Secret {
			t.Errorf("field %d differs: %+v vs %+v", i, f, other)
		}
	}
}
