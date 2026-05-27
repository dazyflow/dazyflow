package daemon

import (
	"context"
	"errors"
	"io"
	"log"
	"sync/atomic"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// renewStub is a JobStore whose Renew returns a fixed error. Only Renew
// is exercised by renewLease, so the embedded nil interface is fine.
type renewStub struct {
	core.JobStore
	err error
}

func (s renewStub) Renew(context.Context, string, string, time.Duration) error { return s.err }

func newLeaseTestWorker(renewErr error) *Worker {
	return &Worker{
		cfg: WorkerConfig{
			ID:              "w",
			LeaseRenewEvery: 2 * time.Millisecond,
			LeaseDuration:   time.Second,
			Logger:          log.New(io.Discard, "", 0),
		},
		store: renewStub{err: renewErr},
	}
}

func TestRenewLease_FencesOnLostOwnership(t *testing.T) {
	for _, lossErr := range []error{core.ErrConflict, core.ErrNotFound} {
		w := newLeaseTestWorker(lossErr)
		var lost atomic.Bool
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// renewLease must invoke onLost (and return) once Renew reports we
		// no longer own the job.
		w.renewLease(ctx, "j1", func() {
			lost.Store(true)
			cancel()
		})
		if !lost.Load() {
			t.Errorf("Renew=%v: onLost should fire (lease lost), but didn't", lossErr)
		}
	}
}

func TestRenewLease_TransientErrorDoesNotFence(t *testing.T) {
	w := newLeaseTestWorker(errors.New("db unreachable"))
	var lost atomic.Bool
	// Short window: several renew ticks all return a transient error, then
	// the context expires. A transient failure must NOT fence — the lease
	// may still be valid.
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	w.renewLease(ctx, "j1", func() { lost.Store(true) })
	if lost.Load() {
		t.Error("transient renew error must not fence execution")
	}
}
