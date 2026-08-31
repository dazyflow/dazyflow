// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/workspace"
)

// savePublished saves a graph AND publishes it, which is what a live flow
// actually looks like: every automatic path (scheduler, webhook, hosted form,
// provider events) refuses an unpublished flow. A test that asserts something
// FIRES must therefore publish, exactly as a user would — before the publish
// rule was made uniform, the webhook/form/event paths fell back to HEAD and
// these fixtures got away with saving only.
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
