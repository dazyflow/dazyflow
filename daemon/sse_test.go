// SPDX-FileCopyrightText: 2026 Angels' Ware
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

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

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

func TestJobEvents_LiveStream_Cov(t *testing.T) {
	t.Parallel()
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

	lines, cancel := sseStream(t, srv.URL, h.token, "/api/v1/me/runs/"+resp.JobID+"/events")
	defer cancel()

	waitForLine(t, lines, "event: snapshot")

	prog := engine.GraphProgress{JobID: resp.JobID, NodeID: "a", Progress: core.Progress{NodeID: "a", Message: "half"}}
	h.bus.Publish(resp.JobID, BusEvent{Progress: &prog})
	waitForLine(t, lines, "event: progress")

	h.bus.Publish(resp.JobID, BusEvent{NodeStatus: &NodeStatusEvent{NodeID: "a", Status: core.JobStatusSucceeded}})
	waitForLine(t, lines, "event: node")

	h.bus.Publish(resp.JobID, BusEvent{Paused: &PausedEvent{NodeID: "a"}})
	waitForLine(t, lines, "event: paused")

	h.bus.Publish(resp.JobID, BusEvent{Terminal: &TerminalEvent{JobID: resp.JobID, Status: core.JobStatusSucceeded}})
	waitForLine(t, lines, "event: terminal")
}

func TestWatchFlowMe_LiveStream_Cov(t *testing.T) {
	t.Parallel()
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

	cancel()
}

func TestWatchFlowMe_NotFound_Cov(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/me/flows/t%2Fws%2Fghost/watch", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("watch ghost = %d, want 404", rw.Code)
	}
}
