package daemon_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/daemon"
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
	// The form must be iframe-able so the "Put this form on my own
	// website" embed snippet works — assert framing is explicitly
	// permitted rather than silently blocked.
	if got := res.Header.Get("Content-Security-Policy"); got != "frame-ancestors *" {
		t.Errorf("CSP = %q, want \"frame-ancestors *\" (form must be embeddable)", got)
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

// TestForm_CollectValuesPassesExtras documents the "Zapier/Make
// attaches extra fields the owner forgot to declare" path: declared
// fields are always present (blank when not posted), and any extras
// the caller attached come through too — no silent drop.
func TestForm_CollectValuesPassesExtras(t *testing.T) {
	declared := []string{"name", "email", "message"}
	posted := url.Values{
		"name":         {"Anna"},
		"email":        {"anna@example.com"},
		"message":      {"hi"},
		"utm_source":   {"facebook"},
		"submitted_at": {"2026-05-28T10:00:00Z"},
	}
	got := daemon.CollectFormValuesForTest(declared, posted)
	for k, want := range map[string]any{
		"name":         "Anna",
		"email":        "anna@example.com",
		"message":      "hi",
		"utm_source":   "facebook",
		"submitted_at": "2026-05-28T10:00:00Z",
	} {
		if got[k] != want {
			t.Errorf("values[%q] = %v, want %v", k, got[k], want)
		}
	}
}

// TestForm_CollectValuesIncludesDeclaredBlanks: a declared field that
// wasn't posted still appears in the seed (as ""), so downstream nodes
// that index by name aren't broken by a missing key.
func TestForm_CollectValuesIncludesDeclaredBlanks(t *testing.T) {
	got := daemon.CollectFormValuesForTest(
		[]string{"name", "email"},
		url.Values{"name": {"Anna"}}, // email not posted
	)
	if got["email"] != "" {
		t.Errorf("declared-but-missing email = %v, want \"\"", got["email"])
	}
	if got["name"] != "Anna" {
		t.Errorf("name = %v, want Anna", got["name"])
	}
}

// TestForm_CollectValuesCaps a flood of extra fields can't bloat the
// store's schema unboundedly. Declared fields go in first so a spammy
// payload can't crowd the owner's own fields out of the cap.
func TestForm_CollectValuesCaps(t *testing.T) {
	declared := []string{"a", "b"}
	posted := url.Values{"a": {"1"}, "b": {"2"}}
	for i := 0; i < 200; i++ {
		posted.Set("extra"+itoa(i), "x")
	}
	got := daemon.CollectFormValuesForTest(declared, posted)
	if len(got) > 50 {
		t.Errorf("got %d values, want <= 50 (cap)", len(got))
	}
	// Owner's declared fields must survive the cap.
	if got["a"] != "1" || got["b"] != "2" {
		t.Errorf("declared fields got crowded out: a=%v b=%v", got["a"], got["b"])
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var s []byte
	for n > 0 {
		s = append([]byte{byte('0' + n%10)}, s...)
		n /= 10
	}
	return string(s)
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
