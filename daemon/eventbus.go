package daemon

import (
	"sync"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// BusEvent is what flows from a worker to subscribers waiting on a job.
// Exactly one of Progress, NodeStatus, Terminal, or Paused is set per event.
type BusEvent struct {
	Progress   *engine.GraphProgress
	NodeStatus *NodeStatusEvent
	Terminal   *TerminalEvent
	Paused     *PausedEvent
}

// PausedEvent fires when a run hits a breakpoint (or steps): execution has
// stopped after NodeID and is holding for Continue/Step. The node's output
// is fully available for inspection while paused. (#12)
type PausedEvent struct {
	NodeID   string `json:"node_id"`
	Stepping bool   `json:"stepping"`
}

// NodeStatusEvent fires whenever a single node-record transitions to a
// new status (succeeded / failed / skipped / awaiting). The UI uses it
// to light up nodes as they execute. Distinct from Progress, which is
// in-flight percent/text updates from within a still-running node.
type NodeStatusEvent struct {
	NodeID string         `json:"node_id"`
	Status core.JobStatus `json:"status"`
	Error  *core.JobError `json:"error,omitempty"`
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
// implementation below is sufficient for a single-node dzd; a multi-node
// deployment would swap in a Redis/NATS-backed Bus so any dzd can serve
// the streaming RPC regardless of which one's worker did the work.
type Bus interface {
	Publish(jobID string, ev BusEvent)
	Subscribe(jobID string) (<-chan BusEvent, func())
}

// localSubscribers is the per-job fan-out machinery shared by MemoryBus
// (which fans out directly in Publish) and PgBus (which fans out from its
// spool drain). It owns the subscriber map and the invariant that sends
// happen under the lock so a concurrent cancel can't close a channel
// mid-send. Embed it; both buses get subscribe/fanout for free.
type localSubscribers struct {
	mu   sync.Mutex
	subs map[string][]chan BusEvent
}

// subscribe registers a buffered channel for jobID's events and returns
// it with an idempotent cancel that deregisters and closes it. The buffer
// (32) plus non-blocking fanout means a slow reader drops events rather
// than backing up the publisher.
func (l *localSubscribers) subscribe(jobID string) (<-chan BusEvent, func()) {
	ch := make(chan BusEvent, 32)
	l.mu.Lock()
	if l.subs == nil {
		l.subs = make(map[string][]chan BusEvent)
	}
	l.subs[jobID] = append(l.subs[jobID], ch)
	l.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			list := l.subs[jobID]
			for i, c := range list {
				if c == ch {
					l.subs[jobID] = append(list[:i], list[i+1:]...)
					break
				}
			}
			if len(l.subs[jobID]) == 0 {
				delete(l.subs, jobID)
			}
			close(ch)
		})
	}
	return ch, cancel
}

// fanout delivers ev to every active subscriber for jobID. Sends happen
// under the lock: cancel() closes a subscriber channel while holding
// l.mu, so sending outside the lock would race that close — and the
// `default` only avoids *blocking* on a full channel, not the panic from
// a send on a *closed* one. Holding the lock across the loop makes send
// and close mutually exclusive; sends are non-blocking, so it stays short.
func (l *localSubscribers) fanout(jobID string, ev BusEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.subs[jobID] {
		select {
		case c <- ev:
		default:
			// Subscriber too slow; drop event. The publisher keeps moving.
		}
	}
}

// MemoryBus fans out events to every active subscriber for a job. Sends
// are non-blocking — a slow subscriber drops events rather than back the
// worker up.
type MemoryBus struct {
	local localSubscribers
}

func NewMemoryBus() *MemoryBus {
	return &MemoryBus{}
}

func (b *MemoryBus) Publish(jobID string, ev BusEvent) {
	b.local.fanout(jobID, ev)
}

func (b *MemoryBus) Subscribe(jobID string) (<-chan BusEvent, func()) {
	return b.local.subscribe(jobID)
}
