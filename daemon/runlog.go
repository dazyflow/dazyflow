package daemon

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Run logs: the persisted, replayable record of what a run SAID while it
// executed — progress lines (a shell drop's stdout, an HTTP drop's "dial
// …"), node status transitions, and the terminal outcome. The live SSE
// stream already carries these to a watching browser; this store is for
// everyone who wasn't watching: `hzctl job logs`, the run-detail page,
// and post-mortems after a 3am scheduled failure.
//
// Capture happens in RecordingBus — a Bus decorator every publisher
// already flows through — so one wire point in hzd records every event
// exactly once (on the replica that produced it; PgBus distribution
// happens beneath the decorator).

// RunLogEntry is one line of a run's log.
type RunLogEntry struct {
	// Seq orders entries within a run and is the resume cursor for
	// streaming. Assigned by the store (monotonic per store, not per
	// run — gaps within a run are fine, order is what matters).
	Seq    int64     `json:"seq"`
	RunID  string    `json:"run_id"`
	TS     time.Time `json:"ts"`
	NodeID string    `json:"node_id,omitempty"`
	// Kind: "progress" (in-flight message from a running node),
	// "status" (node terminal transition), "terminal" (run finished),
	// "truncated" (the per-run cap was hit; later events were dropped).
	Kind string `json:"kind"`
	// Stream labels progress lines from drops that distinguish their
	// output channels ("stdout"/"stderr" — the shell and git drops'
	// data.stream convention). Empty for unlabelled lines.
	Stream  string `json:"stream,omitempty"`
	Message string `json:"message"`
}

// RunLogStore persists run logs. Implementations must be safe for
// concurrent use; Append is called from worker goroutines.
type RunLogStore interface {
	AppendRunLog(ctx context.Context, e RunLogEntry) error
	// ListRunLogs returns entries with Seq > afterSeq in Seq order, at
	// most limit (limit <= 0 = a sane default).
	ListRunLogs(ctx context.Context, runID string, afterSeq int64, limit int) ([]RunLogEntry, error)
}

const (
	// maxRunLogEntries caps how many entries one run may persist — a
	// runaway shell streaming stdout must not grow the table without
	// bound. The live SSE stream is uncapped; only persistence truncates.
	maxRunLogEntries = 5000
	// defaultRunLogPage bounds a single List call.
	defaultRunLogPage = 1000
	// maxRunLogMessage truncates absurdly long single lines.
	maxRunLogMessage = 8 * 1024
)

// MemRunLogStore is the in-process RunLogStore for dev/tests.
type MemRunLogStore struct {
	mu      sync.Mutex
	nextSeq int64
	byRun   map[string][]RunLogEntry
}

func NewMemRunLogStore() *MemRunLogStore {
	return &MemRunLogStore{byRun: map[string][]RunLogEntry{}}
}

func (m *MemRunLogStore) AppendRunLog(_ context.Context, e RunLogEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextSeq++
	e.Seq = m.nextSeq
	m.byRun[e.RunID] = append(m.byRun[e.RunID], e)
	return nil
}

func (m *MemRunLogStore) ListRunLogs(_ context.Context, runID string, afterSeq int64, limit int) ([]RunLogEntry, error) {
	if limit <= 0 {
		limit = defaultRunLogPage
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RunLogEntry, 0, limit)
	for _, e := range m.byRun[runID] {
		if e.Seq <= afterSeq {
			continue
		}
		out = append(out, e)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// RecordingBus decorates a Bus so every published event is also written
// to the RunLogStore. Best-effort by contract: a failed append logs and
// the event still reaches subscribers — the live stream must never
// stall on the log store.
type RecordingBus struct {
	inner Bus
	store RunLogStore
	log   *log.Logger

	mu     sync.Mutex
	counts map[string]int // runID → persisted entries (cap enforcement)
}

func NewRecordingBus(inner Bus, store RunLogStore) *RecordingBus {
	return &RecordingBus{
		inner:  inner,
		store:  store,
		log:    log.New(log.Writer(), "runlog: ", log.LstdFlags),
		counts: map[string]int{},
	}
}

func (b *RecordingBus) Subscribe(jobID string) (<-chan BusEvent, func()) {
	return b.inner.Subscribe(jobID)
}

func (b *RecordingBus) Publish(jobID string, ev BusEvent) {
	if entry, ok := entryForEvent(jobID, ev); ok {
		b.record(jobID, entry, ev.Terminal != nil)
	}
	b.inner.Publish(jobID, ev)
}

func (b *RecordingBus) record(runID string, e RunLogEntry, terminal bool) {
	b.mu.Lock()
	n := b.counts[runID]
	switch {
	case terminal:
		// Terminal always lands (it's the line everyone greps for), and
		// the run's cap counter is done.
		delete(b.counts, runID)
	case n == maxRunLogEntries:
		// First event past the cap: persist one truncation marker, then
		// drop everything until terminal.
		b.counts[runID] = n + 1
		b.mu.Unlock()
		marker := RunLogEntry{
			RunID: runID, TS: e.TS, Kind: "truncated",
			Message: fmt.Sprintf("log truncated after %d entries", maxRunLogEntries),
		}
		if err := b.store.AppendRunLog(context.Background(), marker); err != nil {
			b.log.Printf("append truncation marker for %s: %v", runID, err)
		}
		return
	case n > maxRunLogEntries:
		b.mu.Unlock()
		return
	default:
		b.counts[runID] = n + 1
	}
	b.mu.Unlock()
	// Detached context: Publish callers are often on cancelled run
	// contexts by the time the terminal event flows.
	if err := b.store.AppendRunLog(context.Background(), e); err != nil {
		b.log.Printf("append for %s: %v", runID, err)
	}
}

// entryForEvent renders a BusEvent as a log line. Paused events are
// interactive-debugger chrome, not log content — skipped.
func entryForEvent(runID string, ev BusEvent) (RunLogEntry, bool) {
	now := time.Now().UTC()
	switch {
	case ev.Progress != nil:
		p := ev.Progress.Progress
		msg := p.Message
		// Drops that stream output lines put them in data.line (the
		// LiveConsole convention) — prefer the raw line when present,
		// and keep the stdout/stderr label that rides next to it.
		var stream string
		if line, ok := p.Data["line"].(string); ok && line != "" {
			msg = line
			stream, _ = p.Data["stream"].(string)
		}
		if msg == "" {
			return RunLogEntry{}, false // pure percent ticks aren't log lines
		}
		if len(msg) > maxRunLogMessage {
			msg = msg[:maxRunLogMessage] + "…"
		}
		return RunLogEntry{RunID: runID, TS: now, NodeID: ev.Progress.NodeID, Kind: "progress", Stream: stream, Message: msg}, true
	case ev.NodeStatus != nil:
		msg := string(ev.NodeStatus.Status)
		if ev.NodeStatus.Error != nil {
			msg += ": " + ev.NodeStatus.Error.Message
		}
		return RunLogEntry{RunID: runID, TS: now, NodeID: ev.NodeStatus.NodeID, Kind: "status", Message: msg}, true
	case ev.Terminal != nil:
		msg := string(ev.Terminal.Status)
		if ev.Terminal.Error != nil {
			msg += ": " + ev.Terminal.Error.Message
		}
		return RunLogEntry{RunID: runID, TS: now, Kind: "terminal", Message: msg}, true
	}
	return RunLogEntry{}, false
}

var _ Bus = (*RecordingBus)(nil)

// Prune mirrors the Pg store's retention hook for the in-memory store
// (tests, dev): drop entries older than the cutoff.
func (m *MemRunLogStore) Prune(_ context.Context, olderThan time.Duration, _ int) (int, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	cutoff := time.Now().Add(-olderThan)
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for runID, entries := range m.byRun {
		kept := entries[:0:0]
		for _, e := range entries {
			if e.TS.Before(cutoff) {
				total++
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			delete(m.byRun, runID)
			continue
		}
		m.byRun[runID] = kept
	}
	return total, nil
}
