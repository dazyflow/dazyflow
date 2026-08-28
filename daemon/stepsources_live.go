// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"sync"
	"time"
)

// The LIVE half of a step source: what this process currently has registered,
// and how it is kept in step with the store. stepsources.go holds the other
// half — the naming and URL rules a source's configuration must obey.
//
// Both sources (MCP servers, web APIs) run the same state machine, and it is
// the part where a divergence would be INVISIBLE. A naming rule that drifted
// shows up the first time someone types a name; an applied-map that drifted
// shows up as one org's steps quietly missing on one replica, hours later,
// with nothing in a log to say so. So it is written once and both call it.
//
// What is deliberately NOT here: Save, Delete and DeleteByTenant. Those look
// similar too, but their bodies are mostly validation and prose that belong to
// one source or the other, and a divergence in them is loud — a save is
// refused, a delete returns an error. Hoisting them would trade readable code
// for five closures at each call site.

// stepSourceKey identifies one org-configured step source: the tenant that
// owns it and the name its step ids carry. Shared by both services AND by both
// in-memory stores, so a key can never mean two different things.
type stepSourceKey struct {
	tenant string
	name   string
}

// stepSourceRegistry records what this process has applied.
//
// The value is the UpdatedAt of the row behind each live registration, and
// that is what makes reconcile both cheap and correct across replicas: a row
// whose UpdatedAt still matches is already live with the current configuration
// and is skipped, while an edit made on ANOTHER replica carries a newer
// UpdatedAt and so re-applies here on the next pass.
//
// The zero value is ready to use; the map is built on first write.
type stepSourceRegistry struct {
	mu      sync.Mutex
	applied map[stepSourceKey]time.Time
}

// remember records that k is live as of updated.
func (r *stepSourceRegistry) remember(k stepSourceKey, updated time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.applied == nil {
		r.applied = map[stepSourceKey]time.Time{}
	}
	r.applied[k] = updated
}

// forget drops k, after unregistering it from a catalog. Always paired with
// that unregister: a key left here for a source that is gone would make the
// next reconcile believe it is still current.
func (r *stepSourceRegistry) forget(k stepSourceKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.applied, k)
}

// appliedAt reports when k was applied, and whether it is held at all.
func (r *stepSourceRegistry) appliedAt(k stepSourceKey) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.applied[k]
	return at, ok
}

// appliedKeys snapshots what this process holds. A copy, because the caller
// walks it while calling forget.
func (r *stepSourceRegistry) appliedKeys() []stepSourceKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]stepSourceKey, 0, len(r.applied))
	for k := range r.applied {
		out = append(out, k)
	}
	return out
}

// stepSourcePlan is one source type's answers to what a reconcile pass needs
// to know. Every field is required.
type stepSourcePlan[R any] struct {
	// key, enabled and updatedAt read one stored row.
	key       func(R) stepSourceKey
	enabled   func(R) bool
	updatedAt func(R) time.Time
	// apply makes a row live and, on success, calls remember. It does NOT
	// return an error: what to do with a failure differs by source (an MCP
	// server records it on its row and stays configured; a web API records it
	// and stays unregistered), and in both cases the pass must carry on —
	// one org's bad row must not keep every other org's steps down.
	apply func(context.Context, R)
	// unregister takes a source out of the live catalog. Called for anything
	// this process holds that the store no longer wants.
	unregister func(tenant, name string)
}

// reconcileStepSources makes this process's registrations match rows.
//
// This is what makes both features work on more than one dzd. A source added
// on replica A is a row in Postgres, not a message: replica B sees it on its
// next pass and applies it. A deletion propagates the same way, and so does an
// edit — the row's UpdatedAt no longer matches what this replica applied.
//
// rows is every row in the store, across every tenant, so a row that is absent
// is a row that was deleted. Passing a single tenant's rows here would
// unregister every other tenant's sources.
func reconcileStepSources[R any](ctx context.Context, reg *stepSourceRegistry, rows []R, p stepSourcePlan[R]) {
	desired := make(map[stepSourceKey]struct{}, len(rows))
	for _, row := range rows {
		if !p.enabled(row) {
			// Not in desired, so the loop below takes it down: disabling is
			// the reversible half of deleting and must reach the palette.
			continue
		}
		k := p.key(row)
		desired[k] = struct{}{}
		if at, ok := reg.appliedAt(k); ok && at.Equal(p.updatedAt(row)) {
			continue
		}
		p.apply(ctx, row)
	}
	// Anything this replica holds that the store no longer wants: deleted or
	// disabled, here or on another node.
	for _, k := range reg.appliedKeys() {
		if _, want := desired[k]; want {
			continue
		}
		p.unregister(k.tenant, k.name)
		reg.forget(k)
	}
}

// StepSourceReconcileInterval is how long a change made on another replica may
// take to appear here.
//
// A compromise, and worth naming as one: shorter means a colleague's new
// server or catalog shows up in your palette sooner, longer means fewer
// needless list queries. Thirty seconds is well under the time it takes
// someone to add one and then go looking for its steps.
//
// One value for both sources on purpose: they are configured on the same admin
// flow, and a user should not have to learn two different latencies.
const StepSourceReconcileInterval = 30 * time.Second

// runStepSourceReconciler reconciles now and then on a ticker until ctx ends.
//
// The immediate first pass is the point at boot: a replica that waited a tick
// would serve a palette missing every org's sources for the first half minute.
// A reconcile error at shutdown is not logged — ctx ending mid-pass is the
// normal way to stop, not a fault worth a line in the operator's log.
func runStepSourceReconciler(ctx context.Context, label string, reconcile func(context.Context) error, logf func(string, ...any)) {
	ticker := time.NewTicker(StepSourceReconcileInterval)
	defer ticker.Stop()
	for {
		if err := reconcile(ctx); err != nil && logf != nil && ctx.Err() == nil {
			logf("%s: reconcile: %v", label, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// nowOr reads an overridable clock: the injected one when a test set it, else
// time.Now. Both services carry the same seam.
func nowOr(fn func() time.Time) time.Time {
	if fn != nil {
		return fn()
	}
	return time.Now()
}
