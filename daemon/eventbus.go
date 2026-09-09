// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"sync"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

type BusEvent struct {
	Progress    *engine.GraphProgress
	NodeStatus  *NodeStatusEvent
	Terminal    *TerminalEvent
	Paused      *PausedEvent
	FlowUpdated *FlowUpdatedEvent
}

type FlowUpdatedEvent struct {
	FlowID   string `json:"flow_id"` // tenant/workspace/id
	Commit   string `json:"commit"`
	Author   string `json:"author"`
	Autosave bool   `json:"autosave"`
}

func flowBusKey(tenant, workspace, id string) string {
	return "flow:" + tenant + "/" + workspace + "/" + id
}

type PausedEvent struct {
	NodeID   string `json:"node_id"`
	Stepping bool   `json:"stepping"`
}

type NodeStatusEvent struct {
	NodeID string         `json:"node_id"`
	Status core.JobStatus `json:"status"`
	Error  *core.JobError `json:"error,omitempty"`
	// Set with a skipped status: one of core.SkipCode*, so the editor can say
	// on the card WHY a step went grey rather than leaving it unexplained.
	SkipCode string `json:"skip_code,omitempty"`
}

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
		}
	}
}

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

func (l *localSubscribers) jobIDs() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.subs) == 0 {
		return nil
	}
	out := make([]string, 0, len(l.subs))
	for id := range l.subs {
		out = append(out, id)
	}
	return out
}

func (l *localSubscribers) has(jobID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.subs[jobID]) > 0
}
