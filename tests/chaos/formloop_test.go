// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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

// TestTriggerLoop_IsBroken closed the self-triggering flow: a step calling one
// of OUR OWN urls stamps core.TriggerDepthHeader, the /trigger endpoint reads
// it into the run's TriggerDepth, and seedRun refuses past
// core.MaxTriggerChainDepth.
//
// The hosted form is the same entry point with none of that. handleForm calls
// SubmitGraphWithSeed directly and passes no depth at all, so every form
// submission starts a run at depth 0 — including one a flow's own HTTP step
// posted. The chain never climbs and never breaks.
//
// It is also the WEAKER door of the two: /trigger needs the flow's secret in an
// Authorization header, /form needs nothing. The loop is startable by anyone
// who has the public link.
func TestFormLoop_IsBroken(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	t.Cleanup(func() { hfnet.SetAllowPrivateEgress(false) })

	ks := auth.NewMemKeyStore()
	wsStore, _ := workspace.OpenFS("")
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
	defer ts.Close()
	formURL := ts.URL + "/form/acme/ws1/formloop"

	hfnet.SetSelfOrigin(ts.URL)
	t.Cleanup(func() { hfnet.SetSelfOrigin("") })

	g := core.Graph{
		ID: "formloop", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "intake", Module: "webhook_input", Params: map[string]any{
				"public_form": true,
				"form_fields": []any{"name"},
			}},
			{ID: "again", Module: "http_request", Params: map[string]any{
				"url": formURL, "method": "POST", "body": `{"name":"loop"}`,
				"allow_private_networks": true,
				"headers":                map[string]any{"Content-Type": "application/json"},
			}},
		},
		Edges: []core.Edge{{From: "intake", FromPort: "body", To: "again", ToPort: "request_body"}},
	}
	commit, err := wsStore.Save(g, "qa")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := wsStore.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// One anonymous POST — no bearer token, no secret, nothing but the link.
	req, _ := http.NewRequest("POST", formURL, bytes.NewReader([]byte(`{"name":"kick"}`)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("kick off: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("kickoff status=%d", resp.StatusCode)
	}

	var counts []int
	for range 5 {
		time.Sleep(time.Second)
		recs, _ := jobs.ListGraphRuns(context.Background(), core.ListGraphRunsOpts{Limit: 1000000})
		counts = append(counts, len(recs))
	}
	t.Logf("graph runs after 1..5s: %v (MaxTriggerChainDepth=%d)", counts, core.MaxTriggerChainDepth)
	if counts[len(counts)-1] > counts[0] {
		t.Errorf("FINDING: one anonymous form submission is still spawning runs after 5s (%v) — "+
			"handleForm submits at TriggerDepth 0, so the chain never climbs and never breaks", counts)
	}
}
