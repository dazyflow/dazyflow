// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"sync"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

func TestMemoryBus_DeliversToSubscribers(t *testing.T) {
	t.Parallel()
	b := NewMemoryBus()
	ch1, c1 := b.Subscribe("job-1")
	ch2, c2 := b.Subscribe("job-1")
	defer c1()
	defer c2()

	p := engine.GraphProgress{JobID: "job-1", NodeID: "a"}
	b.Publish("job-1", BusEvent{Progress: &p})

	for _, ch := range []<-chan BusEvent{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Progress == nil || ev.Progress.NodeID != "a" {
				t.Errorf("event = %+v", ev)
			}
		default:
			t.Error("subscriber missed event")
		}
	}
}

func TestMemoryBus_IsolatesJobs(t *testing.T) {
	t.Parallel()
	b := NewMemoryBus()
	ch1, c1 := b.Subscribe("job-1")
	ch2, c2 := b.Subscribe("job-2")
	defer c1()
	defer c2()

	b.Publish("job-1", BusEvent{Terminal: &TerminalEvent{Status: core.JobStatusSucceeded}})

	select {
	case <-ch1:
	default:
		t.Error("job-1 subscriber should have received event")
	}
	select {
	case <-ch2:
		t.Error("job-2 subscriber should not have received job-1 event")
	default:
	}
}

func TestMemoryBus_UnsubscribeCloses(t *testing.T) {
	t.Parallel()
	b := NewMemoryBus()
	ch, cancel := b.Subscribe("job-1")
	cancel()
	if _, ok := <-ch; ok {
		t.Error("channel should be closed after cancel")
	}
}

// TestMemoryBus_PublishCancelRace is the regression test for the
// send-on-closed-channel race: publishers fan out concurrently while
// subscribers churn subscribe→cancel (which closes their channel). On the
// pre-fix code — snapshot under lock, send after unlock — a publish sends
// to a channel cancel() just closed and the runtime panics. With the send
// held under the lock, close and send are mutually exclusive. Passing
// means no panic (and `-race` finds any residual data race).
func TestMemoryBus_PublishCancelRace(t *testing.T) {
	t.Parallel()
	b := NewMemoryBus()
	const job = "job-1"
	stop := make(chan struct{})
	var pubWg, subWg sync.WaitGroup

	for i := 0; i < 4; i++ {
		pubWg.Add(1)
		go func() {
			defer pubWg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					b.Publish(job, BusEvent{Progress: &engine.GraphProgress{JobID: job}})
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		subWg.Add(1)
		go func() {
			defer subWg.Done()
			for j := 0; j < 3000; j++ {
				_, cancel := b.Subscribe(job)
				cancel()
			}
		}()
	}
	subWg.Wait()
	close(stop)
	pubWg.Wait()
}

// TestPgBus_FanoutCancelRace exercises the same race on the PgBus fan-out
// path. fanout/Subscribe touch only the mutex + subs map (no pool), so the
// concurrency contract is testable without a database.
func TestPgBus_FanoutCancelRace(t *testing.T) {
	t.Parallel()
	b := &PgBus{}
	const job = "job-1"
	stop := make(chan struct{})
	var pubWg, subWg sync.WaitGroup

	for i := 0; i < 4; i++ {
		pubWg.Add(1)
		go func() {
			defer pubWg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					b.local.fanout(job, BusEvent{Progress: &engine.GraphProgress{JobID: job}})
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		subWg.Add(1)
		go func() {
			defer subWg.Done()
			for j := 0; j < 3000; j++ {
				_, cancel := b.Subscribe(job)
				cancel()
			}
		}()
	}
	subWg.Wait()
	close(stop)
	pubWg.Wait()
}

func TestMemoryBus_SlowSubscriberDoesNotBlockPublisher(t *testing.T) {
	t.Parallel()
	b := NewMemoryBus()
	ch, cancel := b.Subscribe("job-1")
	defer cancel()

	// Subscriber buffer is 32; flood with more and the publisher must
	// drop without blocking.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			b.Publish("job-1", BusEvent{Progress: &engine.GraphProgress{JobID: "job-1"}})
		}
	}()
	wg.Wait() // returns immediately if drops happen; would deadlock if not

	// At least one event made it through.
	select {
	case <-ch:
	default:
		t.Error("expected at least one delivered event")
	}
}
