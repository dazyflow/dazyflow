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
	// Reader returns the stored value for an exact tenant/name, or ("", nil)
	// when nothing has been stored yet.
	Reader func(ctx context.Context, tenant, name string) (string, error)
	// Writer persists one value.
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

// SetStore installs the read/write pair. nil, nil uninstalls it.
func SetStore(r Reader, w Writer) {
	mu.Lock()
	defer mu.Unlock()
	reader, writer = r, w
}

// Read returns the stored position. Three outcomes, and telling them apart is
// the whole point of this signature:
//
//	("", nil)   nothing stored yet — a genuine first run. Baseline from here.
//	(v,  nil)   the stored position.
//	("", err)   the position could NOT be determined.
//
// A caller must never treat the third case as the first. Read used to fold
// them together and return "" for both, which every dedupe drop then read as
// "first run": one transient store hiccup made a poller re-baseline to
// whatever was in front of it — silently marking as seen everything that had
// arrived since the last successful poll, emitting nothing, and reporting
// success. The mail, feed items or files in that window were skipped
// permanently, because the overwritten watermark said they had been handled.
//
// Failing the step instead loses nothing: the stored position is left exactly
// as it was, so the next poll resumes from it. That is the asymmetry to keep
// in mind — a failed READ must abort before writing, while a failed WRITE is
// safe (at worst the next run re-emits the same batch).
func Read(ctx context.Context, tenant, name string) (string, error) {
	mu.RLock()
	r := reader
	mu.RUnlock()
	if r == nil {
		return "", ErrNoStore
	}
	return r(ctx, tenant, name)
}

// FailRead is the Result a polling drop returns when Read could not tell it
// where it had got to. Built here so every poll source stops the same way and
// says the same thing.
func FailRead(job core.Job, err error) core.Result {
	code, msg, details := Unavailable(err)
	return fail(job, code, msg, details)
}

// FailBaseline is the Result a polling drop returns when it could not persist
// its FIRST position — the baseline run that records where to start watching
// and deliberately emits nothing.
//
// Only the baseline write is worth failing on, and the difference from a
// steady-state write is the whole reason this exists:
//
//   - Steady state: the batch has already gone downstream. A failed write
//     means the next run re-emits it, which is at-least-once and safe, so
//     every drop ignores that error on purpose.
//
//   - Baseline: nothing was emitted, and nothing is recorded. The next run is
//     therefore ALSO a first run, which baselines again. A write that keeps
//     failing parks the flow in "first run" for ever — it emits nothing, ever,
//     and each run looks like a successful empty poll. Failing costs nothing
//     here (there is no batch to discard) and is the only signal the flow is
//     not actually watching anything.
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

// Unavailable turns a Read error into the code, message and technical detail a
// polling drop should fail with. The two causes need different words, and
// neither is the user's fault in a way they could guess from "cursor error".
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

// Write persists value. It is a no-op when no store is wired.
func Write(ctx context.Context, tenant, name, value string) error {
	mu.RLock()
	w := writer
	mu.RUnlock()
	if w == nil {
		return nil
	}
	return w(ctx, tenant, name, value)
}
