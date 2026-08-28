// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// savePublished saves a graph AND publishes it. Every automatic path — the
// scheduler, the /trigger webhook, the hosted form and the provider-event
// fan-outs — refuses an unpublished flow, so an end-to-end test that drives
// one of those has to publish exactly as a user would.
func savePublished(t *testing.T, store *workspace.Store, g core.Graph) {
	t.Helper()
	commit, err := store.Save(g, "test")
	if err != nil {
		t.Fatalf("save %s: %v", g.ID, err)
	}
	if err := store.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit); err != nil {
		t.Fatalf("publish %s: %v", g.ID, err)
	}
}
