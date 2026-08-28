// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func runLogStoreContract(t *testing.T, store RunLogStore) {
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		err := store.AppendRunLog(ctx, RunLogEntry{
			RunID: "run-a", TS: time.Now().UTC(), NodeID: "n1",
			Kind: "progress", Message: fmt.Sprintf("line %d", i),
		})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_ = store.AppendRunLog(ctx, RunLogEntry{RunID: "run-b", TS: time.Now().UTC(), Kind: "terminal", Message: "succeeded"})
	_ = store.AppendRunLog(ctx, RunLogEntry{RunID: "run-b", TS: time.Now().UTC(), NodeID: "sh", Kind: "progress", Stream: "stderr", Message: "warning: deprecated"})

	// Full read, ordered, scoped to the run.
	got, err := store.ListRunLogs(ctx, "run-a", 0, 0)
	if err != nil || len(got) != 5 {
		t.Fatalf("list = %d entries / %v, want 5", len(got), err)
	}
	for i, e := range got {
		if e.Message != fmt.Sprintf("line %d", i+1) {
			t.Errorf("entry %d out of order: %+v", i, e)
		}
		if i > 0 && got[i].Seq <= got[i-1].Seq {
			t.Errorf("seq not monotonic: %d then %d", got[i-1].Seq, got[i].Seq)
		}
	}

	// Cursor resume: everything after entry 3's seq.
	tail, err := store.ListRunLogs(ctx, "run-a", got[2].Seq, 0)
	if err != nil || len(tail) != 2 || tail[0].Message != "line 4" {
		t.Errorf("resume = %+v / %v, want lines 4-5", tail, err)
	}

	// Limit caps the page.
	page, _ := store.ListRunLogs(ctx, "run-a", 0, 2)
	if len(page) != 2 {
		t.Errorf("limited page = %d entries, want 2", len(page))
	}

	// The stream label survives the roundtrip.
	bLogs, err := store.ListRunLogs(ctx, "run-b", 0, 0)
	if err != nil || len(bLogs) != 2 {
		t.Fatalf("run-b list = %d entries / %v, want 2", len(bLogs), err)
	}
	if bLogs[0].Stream != "" || bLogs[1].Stream != "stderr" {
		t.Errorf("streams = %q, %q, want \"\" and \"stderr\"", bLogs[0].Stream, bLogs[1].Stream)
	}

	// Unknown run: empty, not an error.
	none, err := store.ListRunLogs(ctx, "ghost", 0, 0)
	if err != nil || len(none) != 0 {
		t.Errorf("ghost run = %v / %v", none, err)
	}
}

func TestMemRunLogStore(t *testing.T) {
	runLogStoreContract(t, NewMemRunLogStore())
}

// Gated on DAZYFLOW_TEST_DB, like the other Pg store tests.
func TestPgRunLogStore(t *testing.T) {
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres run-log tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	store, err := NewPgRunLogStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgRunLogStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE run_logs"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	runLogStoreContract(t, store)
}

func TestRecordingBus(t *testing.T) {
	store := NewMemRunLogStore()
	bus := NewRecordingBus(NewMemoryBus(), store)
	ctx := context.Background()

	// Subscribers still receive everything (decorator is transparent).
	ch, cancel := bus.Subscribe("run-1")
	defer cancel()

	pct := 0.5
	bus.Publish("run-1", BusEvent{Progress: &engine.GraphProgress{
		JobID: "j1", NodeID: "fetch",
		Progress: core.Progress{Message: "dial smtp.example.com:587", Percent: &pct},
	}})
	bus.Publish("run-1", BusEvent{Progress: &engine.GraphProgress{
		JobID: "j1", NodeID: "sh",
		Progress: core.Progress{Data: map[string]any{"line": "building…", "stream": "stdout"}},
	}})
	bus.Publish("run-1", BusEvent{Progress: &engine.GraphProgress{
		JobID: "j1", NodeID: "sh",
		Progress: core.Progress{Data: map[string]any{"line": "cc: warning", "stream": "stderr"}},
	}})
	// Pure percent tick: streamed, NOT logged.
	bus.Publish("run-1", BusEvent{Progress: &engine.GraphProgress{
		JobID: "j1", NodeID: "fetch", Progress: core.Progress{Percent: &pct},
	}})
	bus.Publish("run-1", BusEvent{NodeStatus: &NodeStatusEvent{NodeID: "fetch", Status: core.JobStatusFailed,
		Error: &core.JobError{Code: "timeout", Message: "exceeded 30s"}}})
	bus.Publish("run-1", BusEvent{Paused: &PausedEvent{NodeID: "fetch"}}) // debugger chrome: not logged
	bus.Publish("run-1", BusEvent{Terminal: &TerminalEvent{JobID: "run-1", Status: core.JobStatusFailed}})

	for i := 0; i < 7; i++ {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("subscriber starved at event %d", i)
		}
	}

	logs, err := store.ListRunLogs(ctx, "run-1", 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []struct{ kind, stream, msg string }{
		{"progress", "", "dial smtp.example.com:587"},
		{"progress", "stdout", "building…"},
		{"progress", "stderr", "cc: warning"},
		{"status", "", "failed: exceeded 30s"},
		{"terminal", "", "failed"},
	}
	if len(logs) != len(want) {
		t.Fatalf("logged %d entries, want %d: %+v", len(logs), len(want), logs)
	}
	for i, w := range want {
		if logs[i].Kind != w.kind || logs[i].Stream != w.stream || logs[i].Message != w.msg {
			t.Errorf("entry %d = %s/%s %q, want %s/%s %q", i, logs[i].Kind, logs[i].Stream, logs[i].Message, w.kind, w.stream, w.msg)
		}
	}
}

func TestRecordingBus_PayloadOptOut(t *testing.T) {
	store := NewMemRunLogStore()
	bus := NewRecordingBus(NewMemoryBus(), store)
	bus.SetLogPayloads(false) // GDPR P2.1: drop content lines
	ctx := context.Background()

	bus.Publish("run-1", BusEvent{Progress: &engine.GraphProgress{
		JobID: "j1", NodeID: "sh",
		Progress: core.Progress{Data: map[string]any{"line": "alice@example.com signed up", "stream": "stdout"}},
	}})
	bus.Publish("run-1", BusEvent{NodeStatus: &NodeStatusEvent{NodeID: "sh", Status: core.JobStatusSucceeded}})
	bus.Publish("run-1", BusEvent{Terminal: &TerminalEvent{JobID: "run-1", Status: core.JobStatusSucceeded}})

	logs, err := store.ListRunLogs(ctx, "run-1", 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Only the status + terminal trail survives; the payload line is dropped.
	for _, e := range logs {
		if e.Kind == "progress" {
			t.Errorf("payload progress line was persisted despite opt-out: %q", e.Message)
		}
	}
	if len(logs) != 2 {
		t.Fatalf("kept %d entries, want 2 (status+terminal): %+v", len(logs), logs)
	}
}

func TestRecordingBus_CapsPerRun(t *testing.T) {
	store := NewMemRunLogStore()
	bus := NewRecordingBus(NewMemoryBus(), store)

	for i := 0; i < maxRunLogEntries+50; i++ {
		bus.Publish("noisy", BusEvent{Progress: &engine.GraphProgress{
			JobID: "j", NodeID: "sh",
			Progress: core.Progress{Message: fmt.Sprintf("line %d", i)},
		}})
	}
	bus.Publish("noisy", BusEvent{Terminal: &TerminalEvent{JobID: "noisy", Status: core.JobStatusSucceeded}})

	logs, _ := store.ListRunLogs(context.Background(), "noisy", 0, maxRunLogEntries+10)
	// cap entries + 1 truncation marker + 1 terminal
	if len(logs) != maxRunLogEntries+2 {
		t.Fatalf("logged %d entries, want %d", len(logs), maxRunLogEntries+2)
	}
	if logs[maxRunLogEntries].Kind != "truncated" {
		t.Errorf("entry %d = %+v, want the truncation marker", maxRunLogEntries, logs[maxRunLogEntries])
	}
	last := logs[len(logs)-1]
	if last.Kind != "terminal" || !strings.Contains(last.Message, "succeeded") {
		t.Errorf("last entry = %+v, want the terminal line", last)
	}
}

func TestRunLogsHTTPEndpoint(t *testing.T) {
	h := newGatewayHarness(t)
	store := NewMemRunLogStore()
	h.svc.RunLogs = store
	_ = h.store.Enqueue(context.Background(), core.JobRecord{
		ID: "run-http", Kind: core.JobKindGraph, GraphID: "f", NodeID: "*",
		Tenant: "t", Workspace: "ws", Status: core.JobStatusRunning,
	})
	for i := 1; i <= 3; i++ {
		_ = store.AppendRunLog(context.Background(), RunLogEntry{
			RunID: "run-http", TS: time.Now().UTC(), NodeID: "a",
			Kind: "progress", Message: fmt.Sprintf("line %d", i),
		})
	}

	rw := h.do(t, "GET", "/api/v1/me/runs/run-http/logs", nil)
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), "line 3") {
		t.Fatalf("logs: %d %s", rw.Code, rw.Body.String())
	}
	// Paging via after + limit.
	rw = h.do(t, "GET", "/api/v1/me/runs/run-http/logs?after=1&limit=1", nil)
	if !strings.Contains(rw.Body.String(), "line 2") || strings.Contains(rw.Body.String(), "line 3") {
		t.Errorf("paged: %s", rw.Body.String())
	}
	// Unknown run → 404; foreign tenant → not visible either.
	if rw := h.do(t, "GET", "/api/v1/me/runs/ghost/logs", nil); rw.Code != 404 {
		t.Errorf("ghost: %d", rw.Code)
	}
	_ = h.store.Enqueue(context.Background(), core.JobRecord{
		ID: "foreign", Kind: core.JobKindGraph, Tenant: "globex", Status: core.JobStatusRunning,
	})
	if rw := h.do(t, "GET", "/api/v1/me/runs/foreign/logs", nil); rw.Code == 200 {
		t.Errorf("foreign run visible: %s", rw.Body.String())
	}
	// Store off → 501.
	h.svc.RunLogs = nil
	if rw := h.do(t, "GET", "/api/v1/me/runs/run-http/logs", nil); rw.Code != 501 {
		t.Errorf("disabled: %d", rw.Code)
	}
}

func TestRunLogPrune(t *testing.T) {
	store := NewMemRunLogStore()
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)
	fresh := time.Now()
	_ = store.AppendRunLog(ctx, RunLogEntry{RunID: "r1", TS: old, Kind: "progress", Message: "ancient"})
	_ = store.AppendRunLog(ctx, RunLogEntry{RunID: "r1", TS: fresh, Kind: "terminal", Message: "recent"})
	_ = store.AppendRunLog(ctx, RunLogEntry{RunID: "r2", TS: old, Kind: "terminal", Message: "all old"})

	n, err := store.Prune(ctx, 24*time.Hour, 0)
	if err != nil || n != 2 {
		t.Fatalf("prune = %d/%v, want 2", n, err)
	}
	left, _ := store.ListRunLogs(ctx, "r1", 0, 0)
	if len(left) != 1 || left[0].Message != "recent" {
		t.Errorf("r1 after prune = %+v", left)
	}
	if gone, _ := store.ListRunLogs(ctx, "r2", 0, 0); len(gone) != 0 {
		t.Errorf("r2 should be fully pruned: %+v", gone)
	}
	// olderThan <= 0 disables.
	if n, _ := store.Prune(ctx, 0, 0); n != 0 {
		t.Errorf("disabled prune removed %d", n)
	}
}

func TestPgRunLogPrune(t *testing.T) {
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres run-log tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	store, err := NewPgRunLogStore(ctx, pool)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE run_logs"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	_ = store.AppendRunLog(ctx, RunLogEntry{RunID: "r1", TS: time.Now().Add(-48 * time.Hour), Kind: "progress", Message: "ancient"})
	_ = store.AppendRunLog(ctx, RunLogEntry{RunID: "r1", TS: time.Now(), Kind: "terminal", Message: "recent"})

	// batch=1: first pass deletes the old row (n == batch → loop again),
	// second pass deletes nothing and terminates — exercises the batching loop.
	n, err := store.Prune(ctx, 24*time.Hour, 1)
	if err != nil || n != 1 {
		t.Fatalf("prune = %d/%v, want 1", n, err)
	}
	left, _ := store.ListRunLogs(ctx, "r1", 0, 0)
	if len(left) != 1 || left[0].Message != "recent" {
		t.Errorf("after prune = %+v", left)
	}
}
