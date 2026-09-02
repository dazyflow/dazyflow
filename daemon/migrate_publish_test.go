// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"io"
	"log"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/workspace"

	_ "github.com/dazyflow/dazyflow/drops" // register the real catalog
)

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func webhookGraph(id string) core.Graph {
	return core.Graph{
		ID: id, Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{
			ID: "in", Module: "webhook_input",
			Params: map[string]any{"secrets": []any{"s"}},
		}},
	}
}

func cronGraph(id string) core.Graph {
	return core.Graph{
		ID: id, Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{
			ID: "t", Module: "cron_trigger",
			Params: map[string]any{"cron": "0 9 * * *"},
		}},
	}
}

func migrateHarness(t *testing.T) (*Service, *workspace.Store) {
	t.Helper()
	store, err := workspace.OpenFS("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	svc := &Service{Workspaces: MapWorkspaces{"acme/ws1": store}}
	return svc, store
}

// The population the migration exists for: an event-triggered flow that was
// firing through the old HEAD fallback. It must come out published, running
// the exact revision it was already running.
func TestMigrateWebhookPublish_PublishesLiveWebhookFlow(t *testing.T) {
	t.Parallel()
	svc, store := migrateHarness(t)
	commit, err := store.Save(webhookGraph("wh"), "test")
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	MigrateWebhookPublish(svc, quietLogger())

	pub, err := store.PublishedCommit("wh")
	if err != nil {
		t.Fatalf("published commit: %v", err)
	}
	if pub != commit {
		t.Errorf("published %q, want the HEAD it was already firing (%q)", pub, commit)
	}
}

// The flows the migration must NOT touch. An unpublished cron flow was never
// live (the scheduler always required publish), so publishing it here would
// START it — turning an upgrade into a surprise run.
func TestMigrateWebhookPublish_LeavesSchedulerDraftAlone(t *testing.T) {
	t.Parallel()
	svc, store := migrateHarness(t)
	if _, err := store.Save(cronGraph("cron"), "test"); err != nil {
		t.Fatalf("save: %v", err)
	}

	MigrateWebhookPublish(svc, quietLogger())

	if pub, _ := store.PublishedCommit("cron"); pub != "" {
		t.Errorf("published an unpublished cron draft (%q) — it was never live", pub)
	}
}

// A node-level `disabled` on the trigger does NOT exclude the flow. That looks
// wrong at first glance and is deliberate: the /trigger endpoint and the event
// fan-outs only consult the WHOLE-FLOW switch, so such a flow was still
// accepting requests and starting runs before this change (the worker then
// records the disabled node as skipped). The migration preserves what was
// happening, not what arguably should have been. Pinned so that if the
// endpoints ever start honouring node-level disable, this fails and the
// migration gets revisited with it.
func TestMigrateWebhookPublish_NodeLevelDisableDoesNotExclude(t *testing.T) {
	t.Parallel()
	svc, store := migrateHarness(t)
	g := webhookGraph("off")
	g.Nodes[0].Disabled = true
	if _, err := store.Save(g, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}

	MigrateWebhookPublish(svc, quietLogger())

	if pub, _ := store.PublishedCommit("off"); pub == "" {
		t.Error("did not publish a flow whose trigger node is disabled, but the " +
			"webhook endpoint still accepts for it — it was live before the change")
	}
}

// The whole-flow switch is a different matter: a paused flow is rejected by
// every endpoint, so it was NOT firing and must stay unpublished.
func TestMigrateWebhookPublish_SkipsPausedFlow(t *testing.T) {
	t.Parallel()
	svc, store := migrateHarness(t)
	g := webhookGraph("paused")
	g.Disabled = true
	if _, err := store.Save(g, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}

	MigrateWebhookPublish(svc, quietLogger())

	if pub, _ := store.PublishedCommit("paused"); pub != "" {
		t.Errorf("published a paused flow (%q) — it was not firing", pub)
	}
}

// Idempotent, and never moves a pointer that already exists: a second run
// must not re-publish an already-published flow at a newer HEAD, or an
// upgrade would silently promote someone's in-progress draft.
func TestMigrateWebhookPublish_IdempotentAndDoesNotAdvancePublished(t *testing.T) {
	t.Parallel()
	svc, store := migrateHarness(t)
	v1, err := store.Save(webhookGraph("wh"), "test")
	if err != nil {
		t.Fatalf("save v1: %v", err)
	}
	MigrateWebhookPublish(svc, quietLogger())

	// The user edits after the migration; HEAD moves, published must not.
	g := webhookGraph("wh")
	g.Nodes = append(g.Nodes, core.Node{ID: "b", Module: "delay", Params: map[string]any{"ms": 1}})
	if _, err := store.Save(g, "test"); err != nil {
		t.Fatalf("save v2: %v", err)
	}

	MigrateWebhookPublish(svc, quietLogger())

	if pub, _ := store.PublishedCommit("wh"); pub != v1 {
		t.Errorf("published moved to %q, want it pinned at %q — the second run must be a no-op", pub, v1)
	}
}

// core.EventTriggerModules is a hand-maintained list, so it rots the moment
// someone adds a new *_on_* trigger drop. Fail here and point at the fix.
func TestEventTriggerModulesMatchCatalog(t *testing.T) {
	t.Parallel()
	// The trigger modules that fire some other way and are handled explicitly
	// by classifyTriggers.
	notEvents := map[string]bool{
		"cron_trigger":        true, // scheduler
		"poll_trigger":        true, // scheduler
		"google_form_trigger": true, // scheduler (interval)
		"webhook_input":       true, // inbound HTTP, checked for a secret/form
	}
	var missing []string
	for id, m := range engine.Default.Manifests() {
		if m.Category != "trigger" || notEvents[id] || core.EventTriggerModules[id] {
			continue
		}
		missing = append(missing, id)
	}
	if len(missing) > 0 {
		t.Errorf("trigger drops missing from core.EventTriggerModules: %v\n"+
			"Add them there (or to notEvents here if they fire via the scheduler), "+
			"or a flow triggered only by one will report as manual-only and the "+
			"upgrade migration will not recognise it as live.", missing)
	}
	// And the reverse: a listed module that no longer exists is dead weight.
	for id := range core.EventTriggerModules {
		if _, ok := engine.Default.Manifests()[id]; !ok {
			t.Errorf("core.EventTriggerModules lists %q, which is not in the catalog", id)
		}
	}
}
