package daemon

import (
	"sync"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func TestMemoryBus_DeliversToSubscribers(t *testing.T) {
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
	b := NewMemoryBus()
	ch, cancel := b.Subscribe("job-1")
	cancel()
	if _, ok := <-ch; ok {
		t.Error("channel should be closed after cancel")
	}
}

func TestMemoryBus_SlowSubscriberDoesNotBlockPublisher(t *testing.T) {
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
