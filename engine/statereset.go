// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import "sync"

// State-reset registry: how the daemon clears a stateful node's persisted
// per-node state (a dedupe cursor, a poll watermark, an HTTP cache) when the
// user hits "Reset state" in the editor. A drop that keeps such state
// registers a key-builder from its init — the same place it calls
// cursor.SetStore — so the *format* of the reserved store keys lives once, in
// the drop package that owns it, and the daemon stays agnostic. Pairs with
// Manifest.NodeState, which is the user-facing half (the label + hint the UI
// shows); this is the machinery half (which keys to delete).

var (
	stateResetMu   sync.RWMutex
	stateResetters = map[string]func(flow, node string) []string{}
)

// RegisterStateReset records, for a module ID, how to compute the reserved
// store keys that hold a node's per-run state, given its (flow, node). Called
// from a drop's init(). Registering twice overwrites — last writer wins, which
// mirrors engine.Register.
func RegisterStateReset(moduleID string, keys func(flow, node string) []string) {
	stateResetMu.Lock()
	defer stateResetMu.Unlock()
	stateResetters[moduleID] = keys
}

func StateResetKeys(moduleID, flow, node string) []string {
	stateResetMu.RLock()
	defer stateResetMu.RUnlock()
	if fn := stateResetters[moduleID]; fn != nil {
		return fn(flow, node)
	}
	return nil
}
