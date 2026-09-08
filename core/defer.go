// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"time"
)

// "Nothing to do until T — requeue me and take other work." Exists because a
// worker is a strictly serial claim → process loop over a small pool, so a step
// that merely SLEEPS occupies one of the daemon's few slots throughout.
//
// Not JobStatusAwaiting: a park waits on an external signal that may never come
// and needs a resume call, while a deferral has a known time and resumes itself
// through Claim's existing AvailableAt horizon.
//
// The module is re-executed when the horizon passes, so it MUST be able to work
// out where it had got to; NodeEnqueuedAt is that anchor.
const StatusDeferred = "deferred"

const ResumeAtOutput = "resume_at"

func Deferred(jobID string, at time.Time) Result {
	return Result{
		JobID:  jobID,
		Status: StatusDeferred,
		Output: map[string]Ref{
			ResumeAtOutput: {MIME: "text/plain", Inline: at.UTC().Format(time.RFC3339Nano)},
		},
	}
}

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

// The anchor a deferring module measures from, and the right one because it does
// NOT move: Requeue preserves EnqueuedAt, so a Wait re-executed after a deferral
// computes the same deadline however many hops it takes.
//
// A ZERO time CLEARS the anchor, which is how an in-process nested run says there
// is no record behind this step: Engine.Run has no queue to requeue into and reads
// any status but "error" as success, so a body step that deferred would report
// done without ever waiting.
func WithNodeEnqueuedAt(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, nodeEnqueuedAtKey{}, at)
}

func NodeEnqueuedAt(ctx context.Context) (time.Time, bool) {
	at, ok := ctx.Value(nodeEnqueuedAtKey{}).(time.Time)
	return at, ok && !at.IsZero()
}

type nodeTimeoutKey struct{}

// The node's OWN declared timeout, not the worker's default backstop. A deferring
// step needs the difference: its execution is instant, so the context deadline no
// longer bounds the wait.
func WithNodeTimeout(ctx context.Context, d time.Duration) context.Context {
	if d <= 0 {
		return ctx
	}
	return context.WithValue(ctx, nodeTimeoutKey{}, d)
}

func NodeTimeout(ctx context.Context) time.Duration {
	d, _ := ctx.Value(nodeTimeoutKey{}).(time.Duration)
	return d
}
