// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"time"
)

// StatusDeferred is the sentinel a module returns to say "nothing to do until
// T — put me back in the queue and take other work." The worker requeues the
// node with AvailableAt = T instead of writing a result, so the node keeps its
// place in the run and no worker slot is held while it waits.
//
// It exists because a worker is a strictly serial claim → process loop and the
// pool is small (DAZYFLOW_WORKER_COUNT defaults to 2), so a step that merely
// SLEEPS occupies one of the daemon's few execution slots for the whole sleep.
// Two parallel Wait steps in one flow could therefore stop every tenant's runs
// for as long as their author typed.
//
// It is not JobStatusAwaiting: a park waits for an external signal that may
// never come and needs a resume call. A deferral has a known time and resumes
// itself, which is exactly what Claim's AvailableAt horizon already does.
//
// The module is re-executed when the horizon passes, so a module that defers
// MUST be able to work out where it had got to. NodeEnqueuedAt is the anchor
// for that: it survives the requeue, so a wait's deadline stays fixed however
// many times the node is re-claimed.
const StatusDeferred = "deferred"

// ResumeAtOutput is the output key carrying an RFC3339 timestamp on a
// StatusDeferred result — when the worker should make the node claimable again.
const ResumeAtOutput = "resume_at"

// Deferred builds the result a module returns to defer itself until at.
func Deferred(jobID string, at time.Time) Result {
	return Result{
		JobID:  jobID,
		Status: StatusDeferred,
		Output: map[string]Ref{
			ResumeAtOutput: {MIME: "text/plain", Inline: at.UTC().Format(time.RFC3339Nano)},
		},
	}
}

// ResumeAt reads the horizon off a deferred result. ok is false when the
// result isn't a deferral or doesn't carry a usable timestamp — the worker
// then treats it as an ordinary completion rather than requeueing a node
// forever on a malformed hint.
func ResumeAt(r Result) (time.Time, bool) {
	if r.Status != StatusDeferred {
		return time.Time{}, false
	}
	ref, ok := r.Output[ResumeAtOutput]
	if !ok {
		return time.Time{}, false
	}
	s, ok := ref.Inline.(string)
	if !ok {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

type nodeEnqueuedAtKey struct{}

// WithNodeEnqueuedAt carries the node record's enqueue time — when the step
// became due, i.e. when its predecessors finished — into the execution
// context. The worker sets it on every node.
//
// It is the anchor a deferring module measures from, and it is the right one
// precisely because it does NOT move: Requeue preserves EnqueuedAt, so a Wait
// re-executed after a deferral computes the same deadline it did the first
// time, however many hops it takes. Measuring from the attempt's start would
// restart the wait on every hop and never finish.
// A ZERO time CLEARS the anchor, which is how an in-process nested run says
// "there is no record behind this step". Engine.Run executes a for_each body
// on the worker's own context but has no queue to requeue into, and it reads
// any status but "error" as success — so a body step that deferred there would
// report done without ever waiting. Presence of the anchor is therefore also
// the signal that deferring is possible at all.
func WithNodeEnqueuedAt(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, nodeEnqueuedAtKey{}, at)
}

// NodeEnqueuedAt returns the anchor set by WithNodeEnqueuedAt. ok is false
// where no job record backs the execution — a loop body, the engine's own
// Engine.Run, a unit harness — and a module must then do its work inline
// rather than defer.
func NodeEnqueuedAt(ctx context.Context) (time.Time, bool) {
	at, ok := ctx.Value(nodeEnqueuedAtKey{}).(time.Time)
	return at, ok && !at.IsZero()
}

type nodeTimeoutKey struct{}

// WithNodeTimeout carries the node's OWN declared TimeoutSeconds (Node.
// TimeoutSeconds) into the execution context — not the worker's default
// backstop, which every node gets and which says nothing about what its author
// asked for.
//
// A deferring step needs the difference. Its execution is instant, so the
// deadline on the context no longer bounds the wait, and silently outliving a
// timeout the author set would be a worse answer than the blocking version
// gave. A step that cannot finish inside a timeout its author DECLARED says so
// at once instead.
func WithNodeTimeout(ctx context.Context, d time.Duration) context.Context {
	if d <= 0 {
		return ctx
	}
	return context.WithValue(ctx, nodeTimeoutKey{}, d)
}

// NodeTimeout returns the declared per-node timeout, or 0 when the node
// declares none (the common case) or the step is running outside a worker.
func NodeTimeout(ctx context.Context) time.Duration {
	d, _ := ctx.Value(nodeTimeoutKey{}).(time.Duration)
	return d
}
