// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// sseStream opens an authenticated SSE GET against a real httptest.Server (so
// the handler sees a genuine, cancellable request context) and returns the
// response, a channel of raw lines, and a cancel func. The reader goroutine
// stops when the context is cancelled or the body closes.
func sseStream(t *testing.T, base, token, path string) (lines <-chan string, cancel func()) {
	t.Helper()
	ctx, cancelCtx := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancelCtx()
		t.Fatalf("sse status = %d", resp.StatusCode)
	}
	ch := make(chan string, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			select {
			case ch <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, cancelCtx
}

// waitForLine reads from ch until it sees a line containing want or times out.
func waitForLine(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case line, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed before seeing %q", want)
			}
			if strings.Contains(line, want) {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

// TestJobEvents_LiveStream_Cov covers jobEvents' non-terminal path: the stream
// stays open after the snapshot, forwards progress / node / terminal frames
// published on the bus, and returns when the terminal frame lands.
func TestJobEvents_LiveStream_Cov(t *testing.T) {
	h := newGatewayHarness(t)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		ServeForTest(h.gw, rw, r)
	}))
	defer srv.Close()

	fid := createFlowViaAPI(t, h, "live", []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 50}}})
	rw := h.do(t, "POST", "/api/v1/me/flows/"+fid+"/run", nil)
	if rw.Code != http.StatusAccepted {
		t.Fatalf("run = %d: %s", rw.Code, rw.Body.String())
	}
	var resp struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)

	// No worker runs in the harness, so the submitted run stays queued
	// (non-terminal) and the stream stays open past the terminal re-read.
	lines, cancel := sseStream(t, srv.URL, h.token, "/api/v1/me/runs/"+resp.JobID+"/events")
	defer cancel()

	waitForLine(t, lines, "event: snapshot")

	// A progress event is forwarded.
	prog := engine.GraphProgress{JobID: resp.JobID, NodeID: "a", Progress: core.Progress{NodeID: "a", Message: "half"}}
	h.bus.Publish(resp.JobID, BusEvent{Progress: &prog})
	waitForLine(t, lines, "event: progress")

	// A node-status event is forwarded.
	h.bus.Publish(resp.JobID, BusEvent{NodeStatus: &NodeStatusEvent{NodeID: "a", Status: core.JobStatusSucceeded}})
	waitForLine(t, lines, "event: node")

	// A paused event is forwarded.
	h.bus.Publish(resp.JobID, BusEvent{Paused: &PausedEvent{NodeID: "a"}})
	waitForLine(t, lines, "event: paused")

	// A terminal event is forwarded and ends the stream.
	h.bus.Publish(resp.JobID, BusEvent{Terminal: &TerminalEvent{JobID: resp.JobID, Status: core.JobStatusSucceeded}})
	waitForLine(t, lines, "event: terminal")
}

// TestWatchFlowMe_LiveStream_Cov covers watchFlowMe end to end: it opens the
// stream (": watching"), forwards a flow_updated frame published on the flow's
// bus key, and disconnects cleanly on context cancel.
func TestWatchFlowMe_LiveStream_Cov(t *testing.T) {
	h := newGatewayHarness(t)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		ServeForTest(h.gw, rw, r)
	}))
	defer srv.Close()

	fid := createFlowViaAPI(t, h, "watchlive", []core.Node{{ID: "a", Module: "noop"}})

	lines, cancel := sseStream(t, srv.URL, h.token, "/api/v1/me/flows/"+fid+"/watch")
	defer cancel()

	waitForLine(t, lines, ": watching")

	h.bus.Publish(flowBusKey("t", "ws", "watchlive"), BusEvent{
		FlowUpdated: &FlowUpdatedEvent{FlowID: "t/ws/watchlive", Commit: "c1", Author: "alice"},
	})
	waitForLine(t, lines, "event: flow_updated")

	// Cancelling the request context ends the handler's select loop.
	cancel()
}

// TestWatchFlowMe_NotFound_Cov covers the early scope/readability guard:
// watching an unknown flow is a clean 404 before any stream opens.
func TestWatchFlowMe_NotFound_Cov(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/me/flows/t%2Fws%2Fghost/watch", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("watch ghost = %d, want 404", rw.Code)
	}
}
