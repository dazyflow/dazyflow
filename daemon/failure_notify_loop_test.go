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

func TestFailureNotify_CarriesTheRunsTriggerDepth(t *testing.T) {
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
		}, FailurePayload{GraphID: "f", RunID: "run-1", ErrorMessage: "boom"})
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

	hfnet.SetSelfOrigin("https://somewhere.else.example")
	t.Cleanup(func() { hfnet.SetSelfOrigin("") })
	if v := fire(srv.URL + "/hook").Get(core.TriggerDepthHeader); v != "" {
		t.Errorf("failure webhook to a third party carried %s: %q", core.TriggerDepthHeader, v)
	}
}
