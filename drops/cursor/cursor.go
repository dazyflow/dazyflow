// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cursor is the per-tenant key/value seam that polling drops use for
// watermarks and dedupe windows. The daemon wires it at startup to the
// encrypted secret store under a reserved "cursor." prefix; a drop only ever
// calls Read and Write.
package cursor

import (
	"context"
	"errors"
	"sync"

	"github.com/dazyflow/dazyflow/core"
)

type (
	Reader func(ctx context.Context, tenant, name string) (string, error)
	Writer func(ctx context.Context, tenant, name, value string) error
)

// ErrNoStore means this deployment wired no cursor store, so a position
// cannot be kept at all. dzd installs the store from the encrypted secret
// store, which needs DAZYFLOW_MASTER_KEY — without one (a dev-mode trial, say)
// there is nowhere to keep a watermark, and a dedupe poller can only ever
// conclude "first run", every run, forever: it silently emits nothing and the
// flow looks like it is working. A drop that needs a position must fail
// loudly on this rather than poll into the void.
var ErrNoStore = errors.New("no cursor store on this deployment (DAZYFLOW_MASTER_KEY unset)")

var (
	mu     sync.RWMutex
	reader Reader
	writer Writer
)

func SetStore(r Reader, w Writer) {
	mu.Lock()
	defer mu.Unlock()
	reader, writer = r, w
}

// Read returns the stored position. Telling the three outcomes apart is the
// whole point of this signature:
//
//	("", nil)   nothing stored yet — a genuine first run. Baseline from here.
//	(v,  nil)   the stored position.
//	("", err)   the position could NOT be determined.
//
// A caller must never treat the third case as the first: re-baselining on a
// transient store failure silently marks everything that arrived since the last
// poll as seen, and the overwritten watermark makes that permanent. Failing the
// step instead loses nothing — the stored position stays as it was.
func Read(ctx context.Context, tenant, name string) (string, error) {
	mu.RLock()
	r := reader
	mu.RUnlock()
	if r == nil {
		return "", ErrNoStore
	}
	return r(ctx, tenant, name)
}

func FailRead(job core.Job, err error) core.Result {
	code, msg, details := Unavailable(err)
	return fail(job, code, msg, details)
}

// FailBaseline is the Result a polling drop returns when it could not persist
// its FIRST position — the baseline run that records where to start watching
// and deliberately emits nothing.
//
// Only the baseline write is worth failing on. A failed steady-state write is
// at-least-once and safe, but a baseline that keeps failing parks the flow in
// "first run" for ever: it emits nothing, and every run looks like a successful
// empty poll.
func FailBaseline(job core.Job, err error) core.Result {
	return fail(job,
		"cursor_baseline_failed",
		"Couldn't record the starting point for what this step should watch, so "+
			"it stopped rather than claim to be watching from a position it can't "+
			"remember. Nothing was emitted and nothing was missed — the next run "+
			"starts again from here.",
		err.Error())
}

func fail(job core.Job, code, msg, details string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: msg, Details: details},
	}
}

func Unavailable(err error) (code, msg, details string) {
	if errors.Is(err, ErrNoStore) {
		return "cursor_no_store",
			"This step remembers what it has already seen, and this deployment " +
				"has nowhere to keep that. Set DAZYFLOW_MASTER_KEY (see the deploy " +
				"guide) or turn off the \"only new\" option to emit every match instead.",
			err.Error()
	}
	return "cursor_unavailable",
		"Could not read what this step has already seen, so it stopped rather " +
			"than risk skipping or repeating items. Nothing was lost — its position " +
			"is untouched and the next run resumes from it.",
		err.Error()
}

func Write(ctx context.Context, tenant, name, value string) error {
	mu.RLock()
	w := writer
	mu.RUnlock()
	if w == nil {
		return nil
	}
	return w(ctx, tenant, name, value)
}
