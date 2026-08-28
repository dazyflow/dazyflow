// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"fmt"
	"runtime/debug"

	"git.sr.ht/~klahr/dazyflow/core"
)

// NativeDrop is a module implemented as Go code, executed by direct function
// call in the same process as the engine. Modules construct one of these and
// hand it to Register during init().
type NativeDrop struct {
	Manifest core.Manifest
	Execute  func(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error)
}

// nativeTransport adapts a NativeDrop to the core.Transport interface. Kept
// unexported — callers should get one via the registry / resolver.
type nativeTransport struct {
	node NativeDrop
}

func (t *nativeTransport) Manifest() core.Manifest { return t.node.Manifest }

// Execute runs the drop's Go function, recovering from any panic so a single
// misbehaving node (a nil-map write, a bad type assertion, an out-of-range
// index) fails just that node instead of crashing the whole daemon process —
// which would take down every other in-flight run, including the per-layer
// goroutines the engine fans out in Run(). The panic is converted into a
// structured StatusError result (the worker treats it like any other node
// failure) and the stack is logged for debugging. Drops are the largest,
// most-churned surface in the tree, so we cannot assume they never panic.
func (t *nativeTransport) Execute(ctx context.Context, job core.Job, progress chan<- core.Progress) (res core.Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			fmt.Printf("dazyflow: drop %q panicked on job %s: %v\n%s\n", t.node.Manifest.ID, job.ID, r, stack)
			res = core.Result{
				JobID:  job.ID,
				Status: core.StatusError,
				Error: &core.JobError{
					Code:    "panic",
					Message: "the step crashed unexpectedly",
					Details: fmt.Sprintf("%v", r),
				},
			}
			err = nil
		}
	}()
	return t.node.Execute(ctx, job, progress)
}
