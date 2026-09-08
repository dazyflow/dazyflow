// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "context"

// How many runs deep a trigger chain already is. Without it a flow whose HTTP
// step calls its own trigger URL runs forever: each iteration is a fresh
// top-level run, so the subgraph depth cap and per-tree fan-out budget — which
// follow parent links inside ONE run tree — never see it.
//
// Only ever sent to this instance's own base URL, so it neither leaks run
// topology to third parties nor depends on a caller honouring it.
const TriggerDepthHeader = "X-Dazyflow-Trigger-Depth"

const MaxTriggerChainDepth = 8

type triggerDepthKey struct{}

func WithTriggerDepth(ctx context.Context, depth int) context.Context {
	if depth <= 0 {
		return ctx
	}
	return context.WithValue(ctx, triggerDepthKey{}, depth)
}

func TriggerDepth(ctx context.Context) int {
	if v, ok := ctx.Value(triggerDepthKey{}).(int); ok {
		return v
	}
	return 0
}
