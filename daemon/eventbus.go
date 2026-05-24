package daemon

import (
	"sync"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

// BusEvent is what flows from a worker to subscribers waiting on a job.
// Exactly one of Progress or Terminal is set per event.
type BusEvent struct {
	Progress *engine.GraphProgress
	Terminal *TerminalEvent
}

// TerminalEvent marks the end of a job. Subscribers should stop reading
// the channel once they see one.
type TerminalEvent struct {
	JobID    string
	Status   core.JobStatus
	Error    *core.JobError
	GraphRes engine.GraphResult
}

// Bus is the contract between workers (publishers) and the API layer
// (subscribers waiting for a graph run to finish). The in-memory
// implementation below is sufficient for a single-node hzd; a multi-node
// deployment would swap in a Redis/NATS-backed Bus so any hzd can serve
// the streaming RPC regardless of which one's worker did the work.
type Bus interface {
	Publish(jobID string, ev BusEvent)
	Subscribe(jobID string) (<-chan BusEvent, func())
}

// MemoryBus fans out events to every active subscriber for a job. Sends
// are non-blocking — a slow subscriber drops events rather than back the
// worker up.
type MemoryBus struct {
	mu   sync.Mutex
	subs map[string][]chan BusEvent
}

func NewMemoryBus() *MemoryBus {
	return &MemoryBus{subs: make(map[string][]chan BusEvent)}
}

func (b *MemoryBus) Publish(jobID string, ev BusEvent) {
	b.mu.Lock()
	chans := append([]chan BusEvent(nil), b.subs[jobID]...)
	b.mu.Unlock()
	for _, c := range chans {
		select {
		case c <- ev:
		default:
			// Subscriber too slow; drop event. The worker keeps moving.
		}
	}
}

func (b *MemoryBus) Subscribe(jobID string) (<-chan BusEvent, func()) {
	ch := make(chan BusEvent, 32)
	b.mu.Lock()
	b.subs[jobID] = append(b.subs[jobID], ch)
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			list := b.subs[jobID]
			for i, c := range list {
				if c == ch {
					b.subs[jobID] = append(list[:i], list[i+1:]...)
					break
				}
			}
			if len(b.subs[jobID]) == 0 {
				delete(b.subs, jobID)
			}
			close(ch)
		})
	}
	return ch, cancel
}
