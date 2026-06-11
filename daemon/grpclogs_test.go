package daemon_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/daemon"
	"git.sr.ht/~klahr/hazyflow/engine"

	controlpb "git.sr.ht/~klahr/hazyflow/api/gen/control"
)

// seedLoggedRun stores a graph-run record and swaps the harness onto a
// RecordingBus + log store, mirroring hzd's production wiring.
func seedLoggedRun(t *testing.T, h *harness, runID, tenant string) *daemon.MemRunLogStore {
	t.Helper()
	store := daemon.NewMemRunLogStore()
	h.svc.RunLogs = store
	h.svc.Bus = daemon.NewRecordingBus(h.svc.Bus, store)

	g := core.Graph{ID: "f1", Tenant: tenant, Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "noop"}}}
	payload, _ := json.Marshal(g)
	if err := h.svc.Jobs.Enqueue(context.Background(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, GraphID: "f1", NodeID: "*",
		Tenant: tenant, Workspace: "ws1", Status: core.JobStatusRunning,
		GraphPayload: payload, Job: core.Job{ID: runID, GraphID: "f1"},
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return store
}

func publishProgress(h *harness, runID, nodeID, msg string) {
	h.svc.Bus.Publish(runID, daemon.BusEvent{Progress: &engine.GraphProgress{
		JobID: runID, NodeID: nodeID, Progress: core.Progress{Message: msg},
	}})
}

func TestGRPC_StreamJobLogs_Replay(t *testing.T) {
	h := newHarnessOpts(t, false)
	defer h.stop()
	seedLoggedRun(t, h, "run-logs-1", "acme")

	publishProgress(h, "run-logs-1", "a", "step one")
	publishProgress(h, "run-logs-1", "a", "step two")

	js := controlpb.NewJobServiceClient(h.conn)
	ctx, cancel := h.ctxWithAuth(t)
	defer cancel()

	stream, err := js.StreamJobLogs(ctx, &controlpb.StreamJobLogsRequest{JobId: "run-logs-1"})
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	var got []*controlpb.JobLogEntry
	for {
		e, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		got = append(got, e)
	}
	if len(got) != 2 || got[0].Message != "step one" || got[1].Message != "step two" {
		t.Fatalf("entries = %+v", got)
	}
	if got[0].Kind != "progress" || got[0].NodeId != "a" || got[0].Seq >= got[1].Seq {
		t.Errorf("entry shape = %+v", got[0])
	}

	// Cursor resume: after the first entry's seq → only the second.
	stream, _ = js.StreamJobLogs(ctx, &controlpb.StreamJobLogsRequest{
		JobId: "run-logs-1", AfterSeq: got[0].Seq,
	})
	e, err := stream.Recv()
	if err != nil || e.Message != "step two" {
		t.Fatalf("resume = %+v / %v", e, err)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("resume tail = %v, want EOF", err)
	}
}

func TestGRPC_StreamJobLogs_Follow(t *testing.T) {
	h := newHarnessOpts(t, false)
	defer h.stop()
	seedLoggedRun(t, h, "run-logs-2", "acme")
	publishProgress(h, "run-logs-2", "a", "before follow")

	js := controlpb.NewJobServiceClient(h.conn)
	ctx, cancel := h.ctxWithAuth(t)
	defer cancel()

	stream, err := js.StreamJobLogs(ctx, &controlpb.StreamJobLogsRequest{JobId: "run-logs-2", Follow: true})
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	type recvResult struct {
		entries []*controlpb.JobLogEntry
		err     error
	}
	done := make(chan recvResult, 1)
	go func() {
		var entries []*controlpb.JobLogEntry
		for {
			e, err := stream.Recv()
			if err == io.EOF {
				done <- recvResult{entries, nil}
				return
			}
			if err != nil {
				done <- recvResult{entries, err}
				return
			}
			entries = append(entries, e)
		}
	}()

	// Give the stream a beat to subscribe, then emit live events.
	time.Sleep(100 * time.Millisecond)
	publishProgress(h, "run-logs-2", "a", "live line")
	h.svc.Bus.Publish("run-logs-2", daemon.BusEvent{Terminal: &daemon.TerminalEvent{
		JobID: "run-logs-2", Status: core.JobStatusSucceeded,
	}})

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("follow stream: %v", res.err)
		}
		var msgs []string
		for _, e := range res.entries {
			msgs = append(msgs, e.Kind+":"+e.Message)
		}
		want := []string{"progress:before follow", "progress:live line", "terminal:succeeded"}
		if len(msgs) != len(want) {
			t.Fatalf("entries = %v, want %v", msgs, want)
		}
		for i := range want {
			if msgs[i] != want[i] {
				t.Errorf("entry %d = %q, want %q", i, msgs[i], want[i])
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow stream never terminated")
	}
}

func TestGRPC_StreamJobLogs_AuthzAndGaps(t *testing.T) {
	h := newHarnessOpts(t, false)
	defer h.stop()
	js := controlpb.NewJobServiceClient(h.conn)
	ctx, cancel := h.ctxWithAuth(t)
	defer cancel()

	// Another tenant's run is invisible (the key is bound to "acme").
	seedLoggedRun(t, h, "foreign-run", "globex")
	stream, _ := js.StreamJobLogs(ctx, &controlpb.StreamJobLogsRequest{JobId: "foreign-run"})
	if _, err := stream.Recv(); err == nil {
		t.Fatal("cross-tenant stream succeeded")
	}

	// Unknown run errors rather than hanging.
	stream, _ = js.StreamJobLogs(ctx, &controlpb.StreamJobLogsRequest{JobId: "ghost"})
	if _, err := stream.Recv(); err == nil {
		t.Fatal("unknown run streamed")
	}
}
