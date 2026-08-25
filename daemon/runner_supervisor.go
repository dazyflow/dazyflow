// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/engine"
)

// Connecting to runners, and staying honest about the ones that are down.
//
// engine.RemoteCatalog.Register dials and asks what the runner serves, so a
// runner that is offline cannot register — and an unregistered runner has no
// drops, which would make its steps vanish from the palette entirely. A flow
// author would see a step they built with simply not exist, with nothing to
// explain it.
//
// So registration is reconciled, not done once: every stored runner is retried
// on a backoff, and what an admin sees is a runner that is REGISTERED BUT
// UNREACHABLE. A broken step is far easier to act on than a missing one.

// RunnerState is what the admin list shows for one runner.
type RunnerState string

const (
	RunnerConnected   RunnerState = "connected"
	RunnerUnreachable RunnerState = "unreachable"
	RunnerDisabled    RunnerState = "disabled"
)

// RunnerStatus is the live view of one registered runner.
type RunnerStatus struct {
	Tenant string      `json:"-"`
	Name   string      `json:"name"`
	State  RunnerState `json:"state"`
	// Drops is what the runner declared when it last connected.
	Drops []string `json:"drops,omitempty"`
	// Error is the last connection failure, verbatim, so an admin can tell a
	// certificate problem from a DNS one without reading the daemon log.
	Error       string    `json:"error,omitempty"`
	LastAttempt time.Time `json:"last_attempt"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	// Attempts since the last success; drives the backoff.
	failures int
}

type runnerRef struct{ tenant, name string }

// Backoff schedule for a runner that will not connect. Capped rather than
// unbounded: a runner that has been down for a day is probably being worked on,
// and an admin who fixes it should not wait an hour to find out.
var runnerBackoff = []time.Duration{
	5 * time.Second,
	15 * time.Second,
	time.Minute,
	5 * time.Minute,
}

func backoffFor(failures int) time.Duration {
	if failures <= 0 {
		return runnerBackoff[0]
	}
	if failures >= len(runnerBackoff) {
		return runnerBackoff[len(runnerBackoff)-1]
	}
	return runnerBackoff[failures]
}

// RunnerSupervisor reconciles the runner table into the engine's catalog.
type RunnerSupervisor struct {
	Runners *Runners
	Catalog *engine.RemoteCatalog
	// Now is overridable for tests; nil means time.Now.
	Now func() time.Time

	mu     sync.Mutex
	status map[runnerRef]*RunnerStatus
	// nextTry gates retries so a Sync on a tight loop doesn't hammer a runner
	// that is down.
	nextTry map[runnerRef]time.Time
}

func NewRunnerSupervisor(runners *Runners, catalog *engine.RemoteCatalog) *RunnerSupervisor {
	return &RunnerSupervisor{
		Runners: runners,
		Catalog: catalog,
		status:  map[runnerRef]*RunnerStatus{},
		nextTry: map[runnerRef]time.Time{},
	}
}

func (s *RunnerSupervisor) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Sync reconciles every stored runner once.
//
// Returns the number connected on this pass. It never returns an error for a
// runner that would not connect — that is a state to display, not a failure of
// the sync — but it does return one if the store itself is unreadable, because
// then nothing can be reconciled at all.
func (s *RunnerSupervisor) Sync(ctx context.Context) (connected int, err error) {
	rows, err := s.Runners.Store.ListAll(ctx)
	if err != nil {
		return 0, fmt.Errorf("list runners: %w", err)
	}
	live := make(map[runnerRef]struct{}, len(rows))
	for _, r := range rows {
		ref := runnerRef{tenant: r.Tenant, name: r.Name}
		live[ref] = struct{}{}
		if s.reconcile(ctx, r, ref) {
			connected++
		}
	}
	// Forget runners that have been deleted from under us.
	s.mu.Lock()
	for ref := range s.status {
		if _, ok := live[ref]; !ok {
			delete(s.status, ref)
			delete(s.nextTry, ref)
		}
	}
	s.mu.Unlock()
	return connected, nil
}

// reconcile brings one runner into the catalog, or records why it could not be.
func (s *RunnerSupervisor) reconcile(ctx context.Context, r Runner, ref runnerRef) bool {
	now := s.now()

	s.mu.Lock()
	st, ok := s.status[ref]
	if !ok {
		st = &RunnerStatus{Tenant: r.Tenant, Name: r.Name}
		s.status[ref] = st
	}
	if !r.Enabled {
		st.State, st.Error = RunnerDisabled, ""
		s.mu.Unlock()
		return false
	}
	// Already connected, and nothing about the row changed: leave it alone
	// rather than re-dialling on every sync.
	if st.State == RunnerConnected {
		s.mu.Unlock()
		return true
	}
	if next, waiting := s.nextTry[ref]; waiting && now.Before(next) {
		s.mu.Unlock()
		return false
	}
	failures := st.failures
	s.mu.Unlock()

	desc, err := s.Runners.Descriptor(ctx, r.Tenant, r.Name)
	if err == nil {
		err = s.Catalog.Register(desc)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	st.LastAttempt = now
	if err != nil {
		st.State = RunnerUnreachable
		st.Error = err.Error()
		st.failures = failures + 1
		s.nextTry[ref] = now.Add(backoffFor(st.failures))
		return false
	}
	st.State = RunnerConnected
	st.Error = ""
	st.failures = 0
	st.LastSuccess = now
	st.Drops = s.Catalog.DropsFor(r.Tenant, r.Name)
	delete(s.nextTry, ref)
	return true
}

// Forget drops a runner's cached state, so the next Sync retries it
// immediately rather than waiting out its backoff. Called after an admin edits
// a registration — the whole point of the edit is usually to fix the failure.
func (s *RunnerSupervisor) Forget(tenant, name string) {
	ref := runnerRef{tenant: tenant, name: name}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.status, ref)
	delete(s.nextTry, ref)
}

// Status returns the live state of one tenant's runners, name-ordered.
func (s *RunnerSupervisor) Status(tenant string) []RunnerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RunnerStatus
	for ref, st := range s.status {
		if ref.tenant != tenant {
			continue
		}
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Run reconciles on a ticker until ctx is done. Every tick is cheap for
// runners that are already connected; the per-runner backoff is what keeps a
// down runner from being dialled on every pass.
func (s *RunnerSupervisor) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 5 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.Sync(ctx); err != nil {
			// The store being unreadable is worth a line; a runner being down
			// is not, since it is already visible in the admin list.
			log.Printf("runner sync: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
