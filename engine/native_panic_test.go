// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestNativeTransport_RecoversPanic proves a drop that panics is converted
// into a structured StatusError result instead of crashing the process — the
// blast-radius containment the per-node recover() exists to provide.
func TestNativeTransport_RecoversPanic(t *testing.T) {
	tp := &nativeTransport{node: NativeDrop{
		Manifest: core.Manifest{ID: "boom"},
		Execute: func(context.Context, core.Job, chan<- core.Progress) (core.Result, error) {
			var m map[string]int
			m["x"] = 1 // panic: assignment to entry in nil map
			return core.Result{}, nil
		},
	}}

	res, err := tp.Execute(context.Background(), core.Job{ID: "job-1"}, nil)
	if err != nil {
		t.Fatalf("recovered Execute should not return an error, got %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want %q", res.Status, core.StatusError)
	}
	if res.Error == nil || res.Error.Code != "panic" {
		t.Fatalf("expected a panic error, got %+v", res.Error)
	}
	if res.JobID != "job-1" {
		t.Fatalf("JobID = %q, want job-1", res.JobID)
	}
}
