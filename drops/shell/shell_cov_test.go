package shell

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestEmitProgress_NilChannel covers the nil-channel guard (no panic, no send).
func TestEmitProgress_NilChannel(t *testing.T) {
	emitProgress(nil, core.Job{ID: "j"}, 0.5, "halfway")
}

// TestEmitProgress_Delivers covers the successful-send branch and the field
// wiring (JobID/NodeID/Percent/Message).
func TestEmitProgress_Delivers(t *testing.T) {
	ch := make(chan core.Progress, 1)
	emitProgress(ch, core.Job{ID: "j", NodeID: "n"}, 0.25, "quarter")
	p := <-ch
	if p.JobID != "j" || p.NodeID != "n" || p.Message != "quarter" {
		t.Fatalf("progress = %+v", p)
	}
	if p.Percent == nil || *p.Percent != 0.25 {
		t.Fatalf("percent = %v", p.Percent)
	}
}

// TestEmitProgress_FullChannelDrops covers the select default: a full channel
// drops the update instead of blocking.
func TestEmitProgress_FullChannelDrops(t *testing.T) {
	ch := make(chan core.Progress) // unbuffered, no reader → send not ready
	emitProgress(ch, core.Job{ID: "j"}, 1.0, "done")
	// If we got here without blocking, the default branch fired.
}

// TestEmitLogProgress_NilChannel covers the nil-channel guard.
func TestEmitLogProgress_NilChannel(t *testing.T) {
	emitLogProgress(nil, core.Job{ID: "j"}, "stdout", "line")
}

// TestEmitLogProgress_Delivers covers the successful-send branch and the Data
// payload wiring.
func TestEmitLogProgress_Delivers(t *testing.T) {
	ch := make(chan core.Progress, 1)
	emitLogProgress(ch, core.Job{ID: "j", NodeID: "n"}, "stderr", "boom")
	p := <-ch
	if p.JobID != "j" || p.NodeID != "n" || p.Message != "boom" {
		t.Fatalf("progress = %+v", p)
	}
	if p.Data["stream"] != "stderr" || p.Data["line"] != "boom" {
		t.Fatalf("data = %+v", p.Data)
	}
}

// TestEmitLogProgress_FullChannelDrops covers the select default branch.
func TestEmitLogProgress_FullChannelDrops(t *testing.T) {
	ch := make(chan core.Progress) // unbuffered, no reader
	emitLogProgress(ch, core.Job{ID: "j"}, "stdout", "x")
}
