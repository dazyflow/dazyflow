package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	_ "git.sr.ht/~klahr/hazy-flow/modules"
)

// TestForEach_E2E_WebhookToIteration drives the full unlock end-to-end:
// a webhook delivers a JSON array, the body is seeded straight into a
// for_each node, and a step runs once per item.
func TestForEach_E2E_WebhookToIteration(t *testing.T) {
	_, wh, jobs, bus, wsStore := startWebhookHarnessLocal(t)

	g := core.Graph{
		ID: "fe-iter", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "inbound", Module: "webhook_input"},
			{ID: "iter", Module: "for_each", Params: map[string]any{
				"step_module": "sleep",
				"step_params": map[string]any{"ms": 1},
				"item_port":   "in",
				"concurrency": 3,
			}},
		},
		Edges: []core.Edge{
			{From: "inbound", FromPort: "body", To: "iter", ToPort: "items"},
		},
		Triggers: []core.GraphTrigger{{Type: "webhook", Secret: "s"}},
	}
	if _, err := wsStore.Save(g, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeWebhookForTest(wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body := []byte(`["alpha","beta","gamma","delta","epsilon"]`)
	req, _ := http.NewRequest("POST", ts.URL+"/trigger/acme/ws1/fe-iter", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer s")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)

	terminal := waitForFire(t, bus, out.JobID, 5*time.Second)
	if terminal != core.JobStatusSucceeded {
		t.Fatalf("status=%q, want succeeded", terminal)
	}

	iter, _ := jobs.Get(t.Context(), daemon.NodeJobID(out.JobID, "iter"))
	if iter.Result == nil {
		t.Fatal("iter has no result")
	}
	results, ok := iter.Result.Output["results"].Inline.([]core.Ref)
	if !ok {
		t.Fatalf("results inline is %T", iter.Result.Output["results"].Inline)
	}
	if len(results) != 5 {
		t.Fatalf("got %d results, want 5", len(results))
	}
	for i, r := range results {
		payload, ok := r.Inline.(map[string]any)
		if !ok {
			t.Fatalf("results[%d].Inline = %T", i, r.Inline)
		}
		if payload["status"] != core.StatusOK {
			t.Errorf("results[%d].status = %v, want ok", i, payload["status"])
		}
	}
	errs, _ := iter.Result.Output["errors"].Inline.(map[string]any)
	if len(errs) != 0 {
		t.Errorf("errors = %+v, want empty", errs)
	}
}
