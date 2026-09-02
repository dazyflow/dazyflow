// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "context"

// TriggerDepthHeader carries how many runs deep a trigger chain already is.
// A step that calls a URL on THIS instance stamps it (see the HTTP drop);
// the trigger endpoints read it and refuse past MaxTriggerChainDepth.
//
// Without it a flow whose HTTP step calls its own trigger URL runs forever:
// each iteration is a fresh top-level run, so the subgraph depth cap and the
// per-tree fan-out budget — which only follow parent links inside one run
// tree — never see it. The same chain across two flows (A fires B, B fires
// A) is the same problem and the same fix.
//
// It is only ever sent to this instance's own base URL, so it neither leaks
// run topology to third parties nor depends on a caller honouring it.
const TriggerDepthHeader = "X-Dazyflow-Trigger-Depth"

// MaxTriggerChainDepth is how many runs one deliberate trigger may set off
// through this instance's own trigger endpoints. Matched to the subgraph
// nesting cap: both answer "a flow is calling itself".
const MaxTriggerChainDepth = 8

type triggerDepthKey struct{}

// WithTriggerDepth carries the current run's trigger-chain depth to the
// steps it executes.
func WithTriggerDepth(ctx context.Context, depth int) context.Context {
	if depth <= 0 {
		return ctx
	}
	return context.WithValue(ctx, triggerDepthKey{}, depth)
}

// TriggerDepth reports the depth carried on ctx; zero when the run was not
// started by another run's step.
func TriggerDepth(ctx context.Context) int {
	if v, ok := ctx.Value(triggerDepthKey{}).(int); ok {
		return v
	}
	return 0
}
