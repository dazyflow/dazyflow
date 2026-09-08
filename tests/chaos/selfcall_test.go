// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

// loopStack is TestFormLoop_IsBroken's fixture, reusable: the real Service +
// Worker behind a live HTTP server that serves the hosted form, so a step's
// outbound call reaches the same daemon a browser would.
type loopStack struct {
	svc  *daemon.Service
	jobs core.JobStore
	ws   *workspace.Store
	ts   *httptest.Server
}

func newLoopStack(t *testing.T) *loopStack {
	t.Helper()
	hfnet.SetAllowPrivateEgress(true)
	t.Cleanup(func() { hfnet.SetAllowPrivateEgress(false) })

	ks := auth.NewMemKeyStore()
	wsStore, err := workspace.OpenFS("")
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"acme/ws1": wsStore},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	wh := daemon.NewWebhookListener(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("/form/", func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeFormForTest(wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	return &loopStack{svc: svc, jobs: jobs, ws: wsStore, ts: ts}
}

func (s *loopStack) formURL(graphID string) string {
	return s.ts.URL + "/form/acme/ws1/" + graphID
}

func (s *loopStack) publish(t *testing.T, g core.Graph) {
	t.Helper()
	commit, err := s.ws.Save(g, "qa")
	if err != nil {
		t.Fatalf("save %s: %v", g.ID, err)
	}
	if err := s.ws.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit); err != nil {
		t.Fatalf("publish %s: %v", g.ID, err)
	}
}

func (s *loopStack) kick(t *testing.T, url string) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, bytes.NewReader([]byte(`{"name":"kick"}`)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("kick off: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("kickoff status=%d", resp.StatusCode)
	}
}

func (s *loopStack) runCounts(samples int) []int {
	var counts []int
	for range samples {
		time.Sleep(time.Second)
		recs, _ := s.jobs.ListGraphRuns(context.Background(), core.ListGraphRunsOpts{Limit: 1000000})
		counts = append(counts, len(recs))
	}
	return counts
}

func TestWebhookSendLoop_IsBroken(t *testing.T) {
	s := newLoopStack(t)
	hfnet.SetSelfOrigin(s.ts.URL)
	t.Cleanup(func() { hfnet.SetSelfOrigin("") })

	formURL := s.formURL("whsendloop")
	s.publish(t, core.Graph{
		ID: "whsendloop", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "intake", Module: "form_input", Params: map[string]any{
				"form_fields": []any{"name"},
			}},
			{ID: "again", Module: "webhook_send", Params: map[string]any{
				"url": formURL, "method": "POST",
				"allow_private_networks": true,
				"content_type":           "application/json",
			}},
		},
		Edges: []core.Edge{{From: "intake", FromPort: "body", To: "again", ToPort: "body"}},
	})

	s.kick(t, formURL)
	counts := s.runCounts(5)
	t.Logf("graph runs after 1..5s: %v (MaxTriggerChainDepth=%d)", counts, core.MaxTriggerChainDepth)
	if counts[len(counts)-1] > counts[0] {
		t.Errorf("FINDING: one anonymous form submission is still spawning runs after 5s (%v) — "+
			"webhook_send never sets core.TriggerDepthHeader, so the chain never climbs", counts)
	}
}

func TestFailureNotifyLoop_IsBroken(t *testing.T) {
	s := newLoopStack(t)
	hfnet.SetSelfOrigin(s.ts.URL)
	t.Cleanup(func() { hfnet.SetSelfOrigin("") })
	formURL := s.formURL("notifyloop")

	s.publish(t, core.Graph{
		ID: "notifyloop", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "intake", Module: "form_input", Params: map[string]any{
				"form_fields": []any{"name"},
			}},
			{ID: "boom", Module: "runner.no_such_step"},
		},
		Edges:         []core.Edge{{From: "intake", FromPort: "body", To: "boom", ToPort: "in"}},
		FailureNotify: &core.FailureNotify{Webhook: formURL},
	})

	s.kick(t, formURL)
	counts := s.runCounts(5)
	t.Logf("graph runs after 1..5s: %v", counts)
	if counts[len(counts)-1] > counts[0] {
		t.Errorf("FINDING: a failing flow whose failure webhook is its own form URL keeps "+
			"submitting itself after 5s (%v) — fireFailureNotification sends no "+
			"core.TriggerDepthHeader and the throttle covers email only", counts)
	}
}

func TestSelfDirected_RecognizesEquivalentURLs(t *testing.T) {
	t.Cleanup(func() { hfnet.SetSelfOrigin("") })
	cases := []struct{ origin, url, why string }{
		{"https://flows.example", "https://flows.example:443/form/acme/ws1/x", "default port written out"},
		{"https://flows.example:443", "https://flows.example/form/acme/ws1/x", "default port left off"},
		{"http://flows.example", "http://flows.example:80/form/acme/ws1/x", "default port written out"},
		{"https://flows.example", "https://flows.example./form/acme/ws1/x", "trailing root dot"},
	}
	for _, c := range cases {
		hfnet.SetSelfOrigin(c.origin)
		if !hfnet.IsSelfDirected(c.url) {
			t.Errorf("FINDING: IsSelfDirected(%q) is false with origin %q (%s) — "+
				"the request reaches our own trigger endpoint without core.TriggerDepthHeader",
				c.url, c.origin, c.why)
		}
	}
}

// The finding above spent: the flow's HTTP
// step posts to its own form through the name the operator did NOT configure
// as the public base URL. Same daemon, same endpoint, and no depth stamp.
//
// Loopback names are the same machine by definition, so registering one
// registers the others; dzd also registers the address it listens on. A
// second PUBLIC name is not something a URL can be asked about — an operator
// with one passes it to net.SetSelfOrigins as well.
func TestAliasedSelfCall_LoopIsBroken(t *testing.T) {
	s := newLoopStack(t)
	hfnet.SetSelfOrigin(strings.Replace(s.ts.URL, "127.0.0.1", "localhost", 1))
	t.Cleanup(func() { hfnet.SetSelfOrigin("") })

	formURL := s.formURL("aliasloop")
	s.publish(t, core.Graph{
		ID: "aliasloop", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "intake", Module: "form_input", Params: map[string]any{
				"form_fields": []any{"name"},
			}},
			{ID: "again", Module: "http_request", Params: map[string]any{
				"url": formURL, "method": "POST", "body": `{"name":"loop"}`,
				"allow_private_networks": true,
				"headers":                map[string]any{"Content-Type": "application/json"},
			}},
		},
		Edges: []core.Edge{{From: "intake", FromPort: "body", To: "again", ToPort: "request_body"}},
	})

	s.kick(t, formURL)
	counts := s.runCounts(5)
	t.Logf("graph runs after 1..5s: %v", counts)
	if counts[len(counts)-1] > counts[0] {
		t.Errorf("FINDING: the self-call loop still runs when the URL spells the origin "+
			"differently from DAZYFLOW_PUBLIC_BASE_URL (%v) — IsSelfDirected is a string compare", counts)
	}
}
