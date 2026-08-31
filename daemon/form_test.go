// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
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
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}, "public_form": true, "form_title": "Contact us"}}},
	}
	savePublished(t, wsStore, g)
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

// TestForm_UnpublishedRendersAPage covers the path a real owner hits first:
// they copy the form link out of the editor and share it before pressing
// Publish. The published revision doesn't exist yet, so this is a 404 — but
// the person reading it is their customer, not an API client, and used to get
// {"error":{"code":"not_found",...}} on a blank page.
//
// The page must NOT say why. "Not published yet" would tell a stranger that
// this flow exists, which is the one thing every 404 in this handler is
// careful not to reveal.
func TestForm_UnpublishedRendersAPage(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	// Saved as a DRAFT only — savePublished is deliberately not called.
	g := core.Graph{
		ID: "draft-only", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"public_form": true}}},
	}
	if _, err := wsStore.Save(g, "test"); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	ts := formServer(t, wh)

	res, err := http.Get(ts.URL + "/form/acme/ws1/draft-only")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (draft has no published revision)", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html — a visitor gets a page, not an API error", ct)
	}
	body, _ := io.ReadAll(res.Body)
	html := string(body)
	if !strings.Contains(html, "may not be live yet") {
		t.Errorf("body missing the unavailable notice, got:\n%s", html)
	}
	// Nothing that names the flow or its state.
	for _, leak := range []string{"draft-only", "publish", "Publish", "not_found"} {
		if strings.Contains(html, leak) {
			t.Errorf("page leaks %q — every miss must look identical to a stranger", leak)
		}
	}
}

func TestForm_NotOptedInIs404(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "private-wh", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}}}}, // no public_form
	}
	savePublished(t, wsStore, g)
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
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}, "public_form": true}}},
	}
	savePublished(t, wsStore, g)
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

// TestForm_DisabledGraphIs404 — a paused flow's hosted form is off, and
// returns 404 (not 403) so a disabled flow is indistinguishable from a
// non-existent one (don't leak which graphs exist).
func TestForm_DisabledGraphIs404(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "paused", Tenant: "acme", Workspace: "ws1", Disabled: true,
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}, "public_form": true}}},
	}
	savePublished(t, wsStore, g)
	ts := formServer(t, wh)
	for _, m := range []string{"GET", "POST"} {
		req, _ := http.NewRequest(m, ts.URL+"/form/acme/ws1/paused", strings.NewReader("name=x"))
		if m == "POST" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s disabled form = %d, want 404", m, res.StatusCode)
		}
	}
}

// TestForm_NoWebhookInputNodeIs400 — a public_form flow with no
// With config on the node, a public form can't exist without a webhook_input
// node (public_form is the node's param). So a flow with no webhook_input node
// has no hosted form — /form 404s, keeping non-public graphs invisible.
func TestForm_NoWebhookInputNodeHidden(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "no-sink", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "x", Module: "delay", Params: map[string]any{"ms": 1}}},
	}
	savePublished(t, wsStore, g)
	ts := formServer(t, wh)
	res, err := http.PostForm(ts.URL+"/form/acme/ws1/no-sink", url.Values{"name": {"x"}})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no webhook_input node → no form)", res.StatusCode)
	}
}

// TestForm_CustomFieldsRendered — declared FormFields override the
// default name/email/message set on the rendered GET page.
func TestForm_CustomFieldsRendered(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "custom", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}, "public_form": true, "form_fields": []string{"phone", "company"}}}},
	}
	savePublished(t, wsStore, g)
	ts := formServer(t, wh)
	res, err := http.Get(ts.URL + "/form/acme/ws1/custom")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	html, _ := io.ReadAll(res.Body)
	for _, want := range []string{`name="phone"`, `name="company"`} {
		if !strings.Contains(string(html), want) {
			t.Errorf("custom form missing %q", want)
		}
	}
	if strings.Contains(string(html), `name="message"`) {
		t.Errorf("custom form should not render the default 'message' field")
	}
}

// TestForm_FieldNameAndTitleEscaped — field names and the title flow
// into the HTML template; html/template must escape them so a field
// name like <script> can't inject markup into the hosted page.
func TestForm_FieldNameAndTitleEscaped(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "xss", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{
			"secrets":     []any{"s"},
			"public_form": true,
			"form_title":  "<script>alert(1)</script>",
			"form_fields": []string{"<img src=x onerror=alert(2)>"},
		}}},
	}
	savePublished(t, wsStore, g)
	ts := formServer(t, wh)
	res, err := http.Get(ts.URL + "/form/acme/ws1/xss")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	html, _ := io.ReadAll(res.Body)
	if strings.Contains(string(html), "<script>alert(1)</script>") {
		t.Errorf("title not escaped — raw <script> present in form HTML")
	}
	if strings.Contains(string(html), "<img src=x onerror=alert(2)>") {
		t.Errorf("field name not escaped — raw <img onerror> present in form HTML")
	}
}

// TestForm_LabelsKeepWhatTheOwnerTyped — field names come out of the owner's
// own "Form fields" box and are read by their customers, so the humanizer is
// only allowed to make them read like labels, never to rewrite them.
//
// Both cases here shipped visible damage to a public page: hyphens were
// replaced with spaces, so a Swedish contact form published "E post"; and the
// first letter was upper-cased by slicing the first BYTE, which turned every
// label starting with a non-ASCII letter into invalid UTF-8.
func TestForm_LabelsKeepWhatTheOwnerTyped(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "labels", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{
			"public_form": true,
			"form_fields": []any{"E-post", "Ärende", "first_name", "what you love about our tea"},
		}}},
	}
	savePublished(t, wsStore, g)
	ts := formServer(t, wh)

	res, err := http.Get(ts.URL + "/form/acme/ws1/labels")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	html := string(body)

	if !utf8.ValidString(html) {
		t.Error("form HTML is not valid UTF-8 — a label was sliced mid-rune")
	}
	for _, want := range []string{
		">E-post<",                      // hyphen survives; it is a letter in this word
		">Ärende<",                      // non-ASCII first letter, already capitalized
		">First name<",                  // underscore is a machine convention, so it goes
		">What you love about our tea<", // sentence gets a capital, nothing else
	} {
		if !strings.Contains(html, want) {
			t.Errorf("label %q missing from the form", want)
		}
	}
	if strings.Contains(html, ">E post<") {
		t.Error(`"E-post" rendered as "E post" — the hyphen was stripped again`)
	}
	// The name/id attributes carry the field name verbatim, so what the flow
	// receives is unaffected by any of the above.
	if !strings.Contains(html, `name="E-post"`) {
		t.Error(`field name attribute should be the owner's text verbatim`)
	}
}

// TestForm_InputTypeFollowsTheFieldName covers the docs' promise that "Email
// and Phone get the matching keyboard on a phone".
//
// It used to compare the whole field name against a short English list, so it
// only ever fired for a field named exactly "email". Every natural phrasing
// missed — "Email address", "Your phone" — and so did every non-English name,
// which is what a Swedish owner's "E-post" hit.
func TestForm_InputTypeFollowsTheFieldName(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	fields := []any{
		"Email", "E-post", "Email address", "Mejladress",
		"Phone", "Telefonnummer", "Mobil",
		"Website", "Hemsida",
		"Name", "Company",
		"Email or phone",
	}
	g := core.Graph{
		ID: "types", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{
			"public_form": true, "form_fields": fields,
		}}},
	}
	savePublished(t, wsStore, g)
	ts := formServer(t, wh)

	res, err := http.Get(ts.URL + "/form/acme/ws1/types")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	html := string(body)

	for field, want := range map[string]string{
		"Email":         "email",
		"E-post":        "email",
		"Email address": "email",
		"Mejladress":    "email",
		"Phone":         "tel",
		"Telefonnummer": "tel",
		"Mobil":         "tel",
		"Website":       "url",
		"Hemsida":       "url",
		"Name":          "text",
		"Company":       "text",
		// Reads as two kinds at once. type=email would make the browser refuse
		// a phone number on submit, so the box that accepts either wins.
		"Email or phone": "text",
	} {
		want := `<input id="` + field + `" name="` + field + `" type="` + want + `"`
		if !strings.Contains(html, want) {
			t.Errorf("missing %s", want)
		}
	}
}

// TestForm_SpeaksTheFlowsLanguage — the hosted form is the flow speaking to a
// stranger, so its own words follow core.Graph.Language (the rule
// internal/maillang documents for an approval email), and <html lang> is set
// from the same resolution so the attribute can't contradict the copy.
//
// Note what is NOT translated, deliberately: the heading and the field labels
// are the OWNER's text, humanized from the field names they typed. Only the
// product's own words — the button, the confirmation, the error banners — come
// from the catalogue.
func TestForm_SpeaksTheFlowsLanguage(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	sv := core.Graph{
		ID: "kontakt", Tenant: "acme", Workspace: "ws1", Language: "sv",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}, "public_form": true, "form_title": "Kontakta oss"}}},
	}
	savePublished(t, wsStore, sv)
	// Language empty means English — the same fallback For() gives.
	en := core.Graph{
		ID: "contact-en", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}, "public_form": true}}},
	}
	savePublished(t, wsStore, en)
	ts := formServer(t, wh)

	get := func(id string) string {
		t.Helper()
		res, err := http.Get(ts.URL + "/form/acme/ws1/" + id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return string(body)
	}

	svHTML := get("kontakt")
	// The honeypot's label is off-screen, so it is only ever READ OUT — which
	// makes it the flow speaking to a visitor like every other string here.
	for _, want := range []string{`<html lang="sv">`, ">Skicka<", "Kontakta oss", "Lämna det här fältet tomt"} {
		if !strings.Contains(svHTML, want) {
			t.Errorf("Swedish form missing %q", want)
		}
	}
	for _, unwanted := range []string{">Submit<", "Leave this field empty"} {
		if strings.Contains(svHTML, unwanted) {
			t.Errorf("Swedish form still shows the English %q", unwanted)
		}
	}

	enHTML := get("contact-en")
	for _, want := range []string{`<html lang="en">`, ">Submit<"} {
		if !strings.Contains(enHTML, want) {
			t.Errorf("English form missing %q", want)
		}
	}

	// The confirmation follows the same language as the form that produced it.
	res, err := http.PostForm(ts.URL+"/form/acme/ws1/kontakt", url.Values{"name": {"Ada"}})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "Tack!") {
		t.Errorf("Swedish confirmation missing \"Tack!\", got: %s", string(body))
	}
}

// TestForm_FailedSubmitKeepsWhatTheVisitorTyped — a submission that can't be
// accepted re-renders the form with a banner and the values still in the
// fields. It used to answer with a plain-text http.Error page, which lost
// everything the visitor had written on the one surface of the product they
// ever see.
//
// The graph here carries an edge to a node that doesn't exist, so core.Validate
// refuses it at submit time — an OWNER-side refusal, which must say "get in
// touch another way" rather than "try again": the next attempt fails too.
func TestForm_FailedSubmitKeepsWhatTheVisitorTyped(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "broken", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}, "public_form": true}}},
		Edges: []core.Edge{{From: "in", FromPort: "body", To: "nope", ToPort: "in"}},
	}
	savePublished(t, wsStore, g)
	ts := formServer(t, wh)

	res, err := http.PostForm(ts.URL+"/form/acme/ws1/broken",
		url.Values{"name": {"Ada Lovelace"}, "email": {"ada@shop.se"}, "message": {"Hej!"}})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (owner-side refusal)", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want an HTML page rather than a plain-text error", ct)
	}
	body, _ := io.ReadAll(res.Body)
	html := string(body)
	// The banner, and the "don't just retry" wording rather than the transient one.
	if !strings.Contains(html, "Something went wrong") {
		t.Errorf("no error banner in the re-rendered form: %s", html)
	}
	if !strings.Contains(html, "get in touch another way") {
		t.Errorf("owner-side refusal should not tell the visitor to try again: %s", html)
	}
	// Every value they typed is back in the form.
	for _, want := range []string{`value="Ada Lovelace"`, `value="ada@shop.se"`, ">Hej!</textarea>"} {
		if !strings.Contains(html, want) {
			t.Errorf("re-rendered form lost %q", want)
		}
	}
	if strings.Contains(html, "Thanks") {
		t.Error("a failed submission must not show the confirmation")
	}
}

// TestForm_UnreadableBodyStillRendersAPage — an unparseable body (or one over
// the 1 MiB cap) is answered with the styled form and a banner, not a bare
// status. Nothing can be re-filled here, because the body never parsed.
func TestForm_UnreadableBodyStillRendersAPage(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "unreadable", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}, "public_form": true}}},
	}
	savePublished(t, wsStore, g)
	ts := formServer(t, wh)

	// "%zz" is not a valid percent-escape, so ParseForm fails.
	res, err := http.Post(ts.URL+"/form/acme/ws1/unreadable",
		"application/x-www-form-urlencoded", strings.NewReader("name=%zz"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	html := string(body)
	if !strings.Contains(html, "Something went wrong") || !strings.Contains(html, "please try again") {
		t.Errorf("expected the transient banner on the re-rendered form, got: %s", html)
	}
	if !strings.Contains(html, "<form method=\"post\">") {
		t.Errorf("expected the form itself to be rendered again, got: %s", html)
	}
}

// TestForm_RefilledValuesAreEscaped — re-filling the fields reflects a
// visitor's input back into the page, which the form never did before. It is
// html/template that makes that safe (contextual escaping inside an attribute
// and inside the textarea), so pin it: a value trying to close its attribute
// or open a tag must come back as text.
func TestForm_RefilledValuesAreEscaped(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "reflect", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}, "public_form": true}}},
		// Invalid on purpose, so the submit fails and the values come back.
		Edges: []core.Edge{{From: "in", FromPort: "body", To: "nope", ToPort: "in"}},
	}
	savePublished(t, wsStore, g)
	ts := formServer(t, wh)

	const attack = `"><script>alert(1)</script>`
	res, err := http.PostForm(ts.URL+"/form/acme/ws1/reflect",
		url.Values{"name": {attack}, "message": {attack}})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	html := string(body)
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Errorf("posted value was reflected unescaped: %s", html)
	}
	// It should still be THERE, just as text — losing it would defeat the point.
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("posted value was dropped rather than escaped: %s", html)
	}
}

// TestForm_JSONBodyIsAcceptedAsFields — someone hand-rolling a form against
// this URL naturally posts JSON. That used to answer 200, fire a run, and
// append a row with every column blank: r.ParseForm() does not error on a
// Content-Type it can't read, it just leaves PostForm empty. Now a flat JSON
// object maps onto the fields exactly as a urlencoded body does.
func TestForm_JSONBodyIsAcceptedAsFields(t *testing.T) {
	_, wh, jobs, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "jsonform", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}, "public_form": true}}},
	}
	savePublished(t, wsStore, g)
	ts := formServer(t, wh)

	res, err := http.Post(ts.URL+"/form/acme/ws1/jsonform", "application/json",
		strings.NewReader(`{"name":"Jane","email":"jane@example.com","message":"Hi","count":3,"ok":true}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	recs, err := jobs.ListByGraph(context.Background(), "jsonform")
	if err != nil {
		t.Fatalf("list by graph: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("expected a run from the JSON submission")
	}
}

// TestForm_ParseFormBodyDecodesEachEncoding pins the decoder itself: this is
// where the blank-row bug lived, so assert the values actually arrive rather
// than only that a run started.
func TestForm_ParseFormBodyDecodesEachEncoding(t *testing.T) {
	post := func(ct, body string) (url.Values, error) {
		r := httptest.NewRequest("POST", "/form/acme/ws1/x", strings.NewReader(body))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		return daemon.ParseFormBodyForTest(r)
	}

	t.Run("urlencoded", func(t *testing.T) {
		got, err := post("application/x-www-form-urlencoded", "name=Jane&message=Hi")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got.Get("name") != "Jane" || got.Get("message") != "Hi" {
			t.Errorf("got %v", got)
		}
	})

	t.Run("flat json", func(t *testing.T) {
		got, err := post("application/json",
			`{"name":"Jane","count":3,"ok":true,"nothing":null,"nested":{"a":1}}`)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		// Scalars keep a readable literal — not 1e+00, not %!s(float64=3).
		if got.Get("name") != "Jane" || got.Get("count") != "3" || got.Get("ok") != "true" {
			t.Errorf("scalars wrong: %v", got)
		}
		if got.Get("nothing") != "" {
			t.Errorf("null should become empty, got %q", got.Get("nothing"))
		}
		// A nested value is kept as JSON text rather than dropped — the column
		// is TEXT anyway, and keeping it beats losing the submission.
		if got.Get("nested") != `{"a":1}` {
			t.Errorf("nested value not preserved: %q", got.Get("nested"))
		}
	})

	t.Run("json case-insensitive content type", func(t *testing.T) {
		got, err := post("Application/JSON; charset=utf-8", `{"name":"Jane"}`)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got.Get("name") != "Jane" {
			t.Errorf("media types are case-insensitive (RFC 9110): %v", got)
		}
	})

	t.Run("unsupported type is refused", func(t *testing.T) {
		if _, err := post("application/xml", "<a/>"); err == nil {
			t.Error("want an error for an encoding the form cannot read")
		}
	})

	t.Run("json that is not an object is refused", func(t *testing.T) {
		// An array has no field names to map onto form fields. Refusing beats
		// silently storing a blank row.
		if _, err := post("application/json", `["Jane"]`); err == nil {
			t.Error("want an error for non-object JSON")
		}
	})
}

// TestForm_UnsupportedContentTypeIsRefused — an encoding the form cannot read
// must be refused loudly. Answering 200 while storing an empty row is the
// worst outcome: the caller is reassured and the owner silently collects junk.
func TestForm_UnsupportedContentTypeIsRefused(t *testing.T) {
	_, wh, jobs, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "xmlform", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}, "public_form": true}}},
	}
	savePublished(t, wsStore, g)
	ts := formServer(t, wh)

	res, err := http.Post(ts.URL+"/form/acme/ws1/xmlform", "application/xml",
		strings.NewReader("<submission><name>Jane</name></submission>"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "urlencoded") {
		t.Errorf("415 should name the encodings that do work, got: %s", body)
	}
	recs, _ := jobs.ListByGraph(context.Background(), "xmlform")
	if len(recs) != 0 {
		t.Errorf("a refused submission must not start a run (got %d)", len(recs))
	}
}

// TestForm_HoneypotDropsSubmission — a bot that fills every input it finds
// completes the hidden field too. It gets the ordinary confirmation (telling
// it otherwise just teaches it to skip the field) but starts no run.
func TestForm_HoneypotDropsSubmission(t *testing.T) {
	_, wh, jobs, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "hp", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}, "public_form": true}}},
	}
	savePublished(t, wsStore, g)
	ts := formServer(t, wh)

	// The trap must actually be on the rendered page, or it can never spring.
	get, err := http.Get(ts.URL + "/form/acme/ws1/hp")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	page, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if !strings.Contains(string(page), daemon.HoneypotFieldNameForTest()) {
		t.Fatalf("honeypot field missing from the rendered form")
	}

	res, err := http.PostForm(ts.URL+"/form/acme/ws1/hp", url.Values{
		"name":                            {"Bot"},
		"message":                         {"buy pills"},
		daemon.HoneypotFieldNameForTest(): {"http://spam.example"},
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (a bot must not learn it was caught)", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "Thanks") {
		t.Errorf("expected the ordinary confirmation, got: %s", body)
	}
	recs, _ := jobs.ListByGraph(context.Background(), "hp")
	if len(recs) != 0 {
		t.Errorf("honeypot submission must not start a run (got %d)", len(recs))
	}
}

// TestForm_LongAnswerFieldsGetATextarea — the old rule matched only the
// literal word "message", so a field an owner actually named ("What you like
// about us") rendered as a one-line box for an obvious paragraph.
func TestForm_LongAnswerFieldsGetATextarea(t *testing.T) {
	_, wh, _, _, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "areas", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "in", Module: "webhook_input", Params: map[string]any{
			"secrets":     []any{"s"},
			"public_form": true,
			"form_fields": []any{"Name", "Email", "What you like about us", "Your feedback"},
		}}},
	}
	savePublished(t, wsStore, g)
	ts := formServer(t, wh)

	res, err := http.Get(ts.URL + "/form/acme/ws1/areas")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	html := string(body)
	if strings.Count(html, "<textarea") != 2 {
		t.Errorf("want 2 textareas (the two prose fields), got %d in: %s",
			strings.Count(html, "<textarea"), html)
	}
	// Name and Email stay single-line, and Email keeps its typed input.
	if !strings.Contains(html, `type="email"`) {
		t.Errorf("Email should still get a typed single-line input")
	}
}

// TestForm_SeedCarriesDeclaredColumnOrder — the columns a submission
// carries downstream are the owner's fields in the order the form drew
// them, not alphabetical.
//
// This is what makes a hosted form fill a readable collection. A row
// writer takes its column order from the value's Headers and only falls
// back to sorting the row keys when none is carried, so a seed with no
// Headers produced "What you like about us | Your email | Your name" for
// a form asking name, email, then the question — while the editor was
// already offering the declared order as the columns. Same order, both
// sides.
func TestForm_SeedCarriesDeclaredColumnOrder(t *testing.T) {
	declared := []string{"Your name", "Your email", "What you like about us"}
	seed := daemon.BuildFormSeedForTest(declared, daemon.CollectFormValuesForTest(
		declared,
		url.Values{
			"Your name":              {"Marina Alvarez"},
			"Your email":             {"marina@example.com"},
			"What you like about us": {"The Earl Grey."},
		},
	))
	got := seed.Output["body"].Headers
	if len(got) != len(declared) {
		t.Fatalf("headers = %v, want %v", got, declared)
	}
	for i, want := range declared {
		if got[i] != want {
			t.Errorf("headers[%d] = %q, want %q (full: %v)", i, got[i], want, got)
		}
	}
}

// TestForm_SeedHeadersKeepExtraFields — a header list names exactly the
// columns that get written, so the extras collectFormValues deliberately
// keeps (utm_source and friends) have to appear in it or they are
// collected and then silently thrown away one step later.
//
// Declared fields keep their order and stay in front; the extras follow
// in sorted order so the same payload twice produces the same columns.
func TestForm_SeedHeadersKeepExtraFields(t *testing.T) {
	declared := []string{"name", "email"}
	seed := daemon.BuildFormSeedForTest(declared, daemon.CollectFormValuesForTest(
		declared,
		url.Values{
			"name":       {"Anna"},
			"email":      {"anna@example.com"},
			"utm_source": {"facebook"},
			"campaign":   {"spring"},
		},
	))
	got := seed.Output["body"].Headers
	want := []string{"name", "email", "campaign", "utm_source"}
	if len(got) != len(want) {
		t.Fatalf("headers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("headers = %v, want %v", got, want)
		}
	}
	// Every collected value must be reachable by a column.
	body, ok := seed.Output["body"].Inline.(map[string]any)
	if !ok {
		t.Fatalf("body inline = %T, want map[string]any", seed.Output["body"].Inline)
	}
	for k := range body {
		found := false
		for _, h := range got {
			if h == k {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("value %q has no column in headers %v — it would be dropped", k, got)
		}
	}
}

// TestForm_SeedHeadersSkipDeclaredFieldsThatWereCapped — collectFormValues
// stops at maxFormFields, so a declared name can be missing from the
// values. A header naming a column with no value would have the writer
// create an empty column, so headers list only what is actually there.
func TestForm_SeedHeadersSkipDeclaredFieldsThatWereCapped(t *testing.T) {
	seed := daemon.BuildFormSeedForTest(
		[]string{"name", "ghost"},
		map[string]any{"name": "Anna"}, // "ghost" never made it into the values
	)
	for _, h := range seed.Output["body"].Headers {
		if h == "ghost" {
			t.Errorf("headers = %v, want no %q (it has no value)", seed.Output["body"].Headers, "ghost")
		}
	}
}
