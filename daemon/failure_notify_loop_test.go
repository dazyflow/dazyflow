// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/engine/jobstore"
)

// The failure webhook is a tenant-supplied URL, and this instance's own form
// and trigger endpoints are URLs like any other. Pointed at its own form, a
// failing flow submitted itself on every failure — a loop with no step in it,
// measured at ~120 runs a second, and the throttle beside it covers the two
// email channels only.
//
// So the notification carries the failed run's place in the chain: the shared
// client stamps depth+1 on a call that comes back to us, and the trigger
// endpoint refuses past core.MaxTriggerChainDepth.
func TestFailureNotify_CarriesTheRunsTriggerDepth(t *testing.T) {
	// No t.Parallel and no egress flip: this sets the process-global self
	// origin, and the package already allows private egress (see TestMain).
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		rw.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	const depth = 3
	ctx := context.Background()
	jobs := jobstore.NewMemory()
	if err := jobs.Enqueue(ctx, core.JobRecord{
		ID: "run-1", Kind: core.JobKindGraph, GraphID: "f",
		Tenant: "t", Workspace: "ws", TriggerDepth: depth,
	}); err != nil {
		t.Fatalf("enqueue run: %v", err)
	}
	svc := &Service{Jobs: jobs}

	fire := func(webhook string) http.Header {
		got = nil
		svc.fireFailureNotification(ctx, core.Graph{
			ID: "f", Tenant: "t", Workspace: "ws",
			FailureNotify: &core.FailureNotify{Webhook: webhook},
		}, FailurePayload{GraphID: "f", RunID: "run-1", ErrorMessage: "boom"}, false)
		if got == nil {
			t.Fatal("the webhook was never called")
		}
		return got
	}

	hfnet.SetSelfOrigin(srv.URL)
	if h := fire(srv.URL + "/form/t/ws/f"); h.Get(core.TriggerDepthHeader) != "4" {
		t.Errorf("failure webhook to our own form sent %q, want depth+1 = 4",
			h.Get(core.TriggerDepthHeader))
	}

	// And nowhere else: the header describes our run topology.
	hfnet.SetSelfOrigin("https://somewhere.else.example")
	t.Cleanup(func() { hfnet.SetSelfOrigin("") })
	if v := fire(srv.URL + "/hook").Get(core.TriggerDepthHeader); v != "" {
		t.Errorf("failure webhook to a third party carried %s: %q", core.TriggerDepthHeader, v)
	}
}
