package flow

import (
	"context"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
)

func TestDelay_Duration(t *testing.T) {
	start := time.Now()
	res, err := executeDelay(t.Context(), core.Job{
		Params: map[string]any{"ms": 100},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err=%+v", res.Status, res.Error)
	}
	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Errorf("slept only %v, want ≥100ms", elapsed)
	}
}

func TestDelay_Cancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	res, err := executeDelay(ctx, core.Job{
		Params: map[string]any{"ms": 5000},
	}, nil)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if res.Status != core.StatusError {
		t.Errorf("status = %q, want error", res.Status)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("sleep did not honor cancellation; elapsed=%v", time.Since(start))
	}
}

func TestDelay_BadParam(t *testing.T) {
	res, _ := executeDelay(t.Context(), core.Job{
		Params: map[string]any{},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
}

func TestDelay_Passthrough(t *testing.T) {
	res, err := executeDelay(t.Context(), core.Job{
		Params: map[string]any{"ms": 10},
		Input:  map[string]core.Ref{"in": {Ref: "x", MIME: "text/plain"}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Output["out"].Ref != "x" {
		t.Errorf("passthrough failed: %+v", res.Output)
	}
}
