// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"log"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// MigrateWebhookPublish publishes every flow that was relying on the old
// fall-back-to-HEAD behaviour, so upgrading doesn't silently take live
// webhooks offline.
//
// Before: the scheduler required a published commit, but the webhook, hosted
// form and inbound-event paths called Store.LoadPublishedOrHead — so a flow
// that had NEVER been published still fired, running its draft. "Published"
// therefore meant two different things depending on the trigger.
//
// After: every automatic path requires a published revision. That is the
// consistent rule, but it is also a behaviour change for exactly one
// population — flows with an event trigger and no published commit, which
// were receiving yesterday and would go dark today. This publishes their
// current HEAD once, which is precisely the revision they were already
// running, so the upgrade is a no-op from the flow's point of view.
//
// Scope is deliberately narrow. A flow with only a cron/poll trigger is NOT
// touched: the scheduler already refused to fire it unpublished, so it was
// never live and publishing it here would START it — turning a migration into
// a surprise. Same for flows with no trigger at all.
//
// Idempotent: a second run finds every affected flow already published and
// does nothing. Safe to call on every boot, which is how it is wired.
func MigrateWebhookPublish(svc *Service, logger *log.Logger) {
	if svc == nil {
		return
	}
	enum, ok := svc.Workspaces.(WorkspaceEnumerator)
	if !ok {
		// A registry that can't enumerate (a lazy remote one) can't be
		// migrated here; those deployments publish through their own tooling.
		return
	}
	published, skipped := 0, 0
	for key, store := range enum.All() {
		if store == nil {
			continue
		}
		ids, err := store.ListGraphs()
		if err != nil {
			logger.Printf("publish-migration: list %s: %v", key, err)
			continue
		}
		for _, id := range ids {
			commit, err := store.PublishedCommit(id)
			if err != nil {
				logger.Printf("publish-migration: published-commit %s/%s: %v", key, id, err)
				continue
			}
			if commit != "" {
				continue // already published — nothing to preserve
			}
			g, err := store.Load(id)
			if err != nil {
				logger.Printf("publish-migration: load %s/%s: %v", key, id, err)
				continue
			}
			// A paused flow is rejected by every endpoint, so it was not firing
			// and there is nothing to preserve. Skipping also avoids a nasty
			// surprise later: publish it now and un-pausing would make a months
			// -old revision live instantly, with no publish step in between.
			if g.Disabled || !firesWithoutScheduler(g) {
				skipped++
				continue
			}
			if err := store.PromoteToEnvironment(id, workspace.PublishedEnv, "HEAD"); err != nil {
				logger.Printf("publish-migration: publish %s/%s: %v", key, id, err)
				continue
			}
			published++
			logger.Printf("publish-migration: published %s/%s — it had an event trigger and no published revision, so it was firing HEAD", key, id)
		}
	}
	if published > 0 || skipped > 0 {
		logger.Printf("publish-migration: published %d flow(s) that were live via the old HEAD fallback; left %d unpublished draft(s) alone", published, skipped)
	}
}

// firesWithoutScheduler reports whether a flow has a trigger that used to fire
// through the HEAD fallback — a reachable webhook (secret key or hosted form)
// or an inbound provider event. Cron and poll triggers are excluded on
// purpose: the scheduler already gated those on publish, so an unpublished one
// was never live and must not be started by a migration.
//
// A node-level `disabled` on the trigger does NOT exclude a flow, because it
// did not stop the request being accepted before this change either — the
// endpoint and the event fan-outs only honour the whole-flow switch. The
// migration preserves what was actually happening, not what arguably should
// have been.
func firesWithoutScheduler(g core.Graph) bool {
	return core.HasConfiguredWebhookTrigger(g) || core.HasEventTrigger(g)
}
