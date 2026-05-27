package daemon_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
)

// formServer stands the /form handler up on an httptest server using
// the same dispatch trick the webhook tests use.
func formServer(t *testing.T, wh *daemon.WebhookListener) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/form/", func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeFormForTest(wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestForm_GETRendersOptedInForm(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "contact", Tenant: "acme", Workspace: "ws1",
		Nodes:    []core.Node{{ID: "in", Module: "webhook_input"}},
		Triggers: []core.GraphTrigger{{Type: "webhook", Secret: "s", PublicForm: true, FormTitle: "Contact us"}},
	}
	if _, err := wsStore.Save(g, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}
	ts := formServer(t, wh)

	res, err := http.Get(ts.URL + "/form/acme/ws1/contact")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	html := string(body)
	for _, want := range []string{"Contact us", `name="name"`, `name="email"`, `name="message"`, "<textarea"} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered form missing %q", want)
		}
	}
}

func TestForm_NotOptedInIs404(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "private-wh", Tenant: "acme", Workspace: "ws1",
		Nodes:    []core.Node{{ID: "in", Module: "webhook_input"}},
		Triggers: []core.GraphTrigger{{Type: "webhook", Secret: "s"}}, // no PublicForm
	}
	if _, err := wsStore.Save(g, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}
	ts := formServer(t, wh)

	res, err := http.Get(ts.URL + "/form/acme/ws1/private-wh")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (form not opted in)", res.StatusCode)
	}
}

func TestForm_POSTSubmitsRun(t *testing.T) {
	_, wh, jobs, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "contact2", Tenant: "acme", Workspace: "ws1",
		Nodes:    []core.Node{{ID: "in", Module: "webhook_input"}},
		Triggers: []core.GraphTrigger{{Type: "webhook", Secret: "s", PublicForm: true}},
	}
	if _, err := wsStore.Save(g, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}
	ts := formServer(t, wh)

	form := url.Values{"name": {"Jane"}, "email": {"jane@example.com"}, "message": {"Hello"}}
	res, err := http.PostForm(ts.URL+"/form/acme/ws1/contact2", form)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "Thanks") {
		t.Errorf("expected thank-you page, got: %s", string(body))
	}
	// A run should have been created for the graph.
	recs, err := jobs.ListByGraph(context.Background(), "contact2")
	if err != nil {
		t.Fatalf("list by graph: %v", err)
	}
	if len(recs) == 0 {
		t.Errorf("expected a run to be submitted by the form POST")
	}
}
