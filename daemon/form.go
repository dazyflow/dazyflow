// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)

// defaultFormFields is the contact-form shape a hosted form falls back
// to when the trigger doesn't name its own fields. Covers the canonical
// "website contact form → somewhere" use case out of the box.
var defaultFormFields = []string{"name", "email", "message"}

// handleForm serves the hosted intake form: a public page a
// non-technical user can point people at without anyone needing a
// bearer token or a curl command. GET renders the form; POST accepts a
// submission and fires the flow with the field values as webhook_input
// body. Only graphs whose webhook trigger sets public_form=true expose
// anything here — every other path is a 404.
//
// This is the "first-class intake source" that closes the biggest
// non-technical gap: the webhook /trigger endpoint needs an Authorization
// header most form tools can't send, whereas this page needs nothing but
// a link.
func (w *WebhookListener) handleForm(rw http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/form/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		http.Error(rw, "expected /form/<tenant>/<workspace>/<graph-id>", http.StatusBadRequest)
		return
	}
	tenant, workspace, graphID := parts[0], parts[1], parts[2]

	store, err := w.svc.Workspaces.Open(tenant, workspace)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	// Hosted form submissions run the published revision (HEAD fallback
	// for never-published flows), matching the webhook listener.
	g, err := store.LoadPublishedOrHead(graphID)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	if g.Disabled {
		// Symmetric with the webhook: a paused flow's form is off.
		// Use 404 (not 403) to match the rest of this handler's
		// "don't reveal whether the graph exists" stance.
		http.NotFound(rw, r)
		return
	}
	fields, title, ok := publicFormConfig(g)
	if !ok {
		// Either no webhook_input node or it hasn't opted into a public form.
		// Don't reveal which — a plain 404 keeps non-public graphs invisible.
		http.NotFound(rw, r)
		return
	}
	if len(fields) == 0 {
		fields = defaultFormFields
	}
	if title == "" {
		title = g.Name
	}
	if title == "" {
		title = g.ID
	}

	switch r.Method {
	case http.MethodGet:
		renderForm(rw, formView{Title: title, Fields: fields, Submitted: false})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(rw, "could not read form", http.StatusBadRequest)
			return
		}
		values := collectFormValues(fields, r.PostForm)
		seed := buildFormSeed(values)
		seeds := map[string]core.Result{}
		for _, n := range g.Nodes {
			if n.Module == webhookInputModuleID {
				seeds[n.ID] = seed
			}
		}
		if len(seeds) == 0 {
			http.Error(rw, "this form's flow has no webhook step to receive it", http.StatusBadRequest)
			return
		}
		principal := SystemPrincipal("dazyflow-form", g.Tenant, g.Workspace)
		runID, err := w.svc.SubmitGraphWithSeed(r.Context(), principal, g, seeds)
		if err != nil {
			w.logger.Printf("form submit %s/%s/%s: %v", tenant, workspace, graphID, err)
			http.Error(rw, "could not submit the form", http.StatusInternalServerError)
			return
		}
		w.logger.Printf("form %s/%s/%s → %s", tenant, workspace, graphID, runID)
		renderForm(rw, formView{Title: title, Submitted: true})
	default:
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// maxFormFields caps the total field count on a single hosted-form
// submission. The Collections store auto-evolves columns from whatever
// gets posted (so the owner doesn't manage schema), so an unbounded
// payload would let a spammy caller bloat the workspace's schema with
// 1000s of TEXT columns. 50 is well above what Zapier / Make /
// Typeform / Squarespace actually attach in practice (typically <20).
const maxFormFields = 50

// collectFormValues builds the {field: value} map seeded into the
// flow's webhook_input.body port from a hosted-form POST. It accepts
// every posted field (Zapier, Make, Typeform attach utm_*, source,
// submitted_at etc. that owners commonly forget to declare), not just
// the ones named in form_fields — the old "declared-only" filter
// dropped extras silently while the visitor saw "Thanks!", which was
// the worst combination: visitor reassured, owner blind, payload
// truncated.
//
// declared fields are always present (blank when missing) so
// downstream nodes that read body.email by name don't have to defend
// against absent keys. They're inserted first so they're never crowded
// out of maxFormFields by a payload that pads itself with junk.
func collectFormValues(declared []string, posted url.Values) map[string]any {
	out := make(map[string]any, len(declared)+8)
	for _, f := range declared {
		out[f] = posted.Get(f)
	}
	for k, v := range posted {
		if len(out) >= maxFormFields {
			break
		}
		if k == "" {
			continue
		}
		if _, already := out[k]; already {
			continue
		}
		if len(v) > 0 {
			out[k] = v[0]
		} else {
			out[k] = ""
		}
	}
	return out
}

// publicFormConfig returns the hosted-form fields and title from the graph's
// webhook_input node when it has opted into a public form (the node's
// public_form param), else ok=false. Config lives on the node now — the
// Triggers menu is gone.
func publicFormConfig(g core.Graph) (fields []string, title string, ok bool) {
	for _, n := range g.Nodes {
		if n.Module != webhookInputModuleID {
			continue
		}
		if pf, _ := n.Params["public_form"].(bool); !pf {
			continue
		}
		t, _ := n.Params["form_title"].(string)
		return formStringSlice(n.Params["form_fields"]), t, true
	}
	return nil, "", false
}

// formStringSlice coerces a node param into a []string, tolerating the []any
// of strings that JSON unmarshalling produces (and an already-typed []string).
func formStringSlice(v any) []string {
	switch arr := v.(type) {
	case []string:
		return arr
	case []any:
		out := make([]string, 0, len(arr))
		for _, it := range arr {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// buildFormSeed mirrors buildWebhookSeed's output shape (body + headers
// ports) but takes an already-parsed field map, so webhook_input
// downstream sees the same {key: value} object it would from a JSON
// webhook POST — i.e. ${trigger.body.email} works identically whether
// the data arrived via the hosted form or a real webhook.
func buildFormSeed(values map[string]any) core.Result {
	return core.Result{
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"body":    {MIME: "application/json", Inline: values},
			"headers": {MIME: "application/json", Inline: map[string]any{}},
		},
	}
}

type formView struct {
	Title     string
	Fields    []string
	Submitted bool
}

// humanizeField turns a field key ("first_name") into a label
// ("First name") for the form. Mirrors the frontend's humanize so the
// hosted form reads like the rest of the product.
func humanizeField(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "_", " "), "-", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func renderForm(rw http.ResponseWriter, v formView) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Allow the form to be iframed onto any site — the "Put this form on
	// my own website" embed snippet relies on it. Set frame-ancestors
	// explicitly (rather than leaning on the absence of X-Frame-Options)
	// so the intent is deliberate. Clickjacking risk is low: the form is
	// secret-less and submits only public, owner-declared fields.
	rw.Header().Set("Content-Security-Policy", "frame-ancestors *")
	if err := formTemplate.Execute(rw, v); err != nil {
		// Headers may already be written; nothing useful to do but log
		// via the default logger and bail.
		fmt.Fprintf(rw, "<!-- render error: %v -->", err)
	}
}

// formTemplate is a self-contained, dependency-free HTML page. html/template
// escapes every interpolation, so field names and titles can't inject
// markup. "message" (and any field whose name contains "message") gets a
// textarea; everything else a single-line input, with email/name getting
// the matching input type for nicer mobile keyboards.
var formTemplate = template.Must(template.New("form").Funcs(template.FuncMap{
	"label":     humanizeField,
	"inputType": formInputType,
	"isArea":    func(f string) bool { return strings.Contains(strings.ToLower(f), "message") },
}).Parse(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root{color-scheme:light dark}
body{font-family:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;max-width:480px;margin:0 auto;padding:48px 20px;color:#1a1730;background:#fbfaff}
@media(prefers-color-scheme:dark){body{color:#e6e2f5;background:#0f0d1c}input,textarea{background:#1c1930;color:#e6e2f5;border-color:#332d52}}
h1{font-size:22px}
label{display:block;margin:16px 0 4px;font-weight:600;font-size:14px}
input,textarea{width:100%;padding:10px 12px;border:1px solid #cfc7ea;border-radius:8px;font:inherit;box-sizing:border-box}
textarea{min-height:120px;resize:vertical}
button{margin-top:20px;padding:11px 18px;border:0;border-radius:8px;background:#6d5dff;color:#fff;font:inherit;font-weight:600;cursor:pointer}
button:hover{background:#5a49e6}
.done{padding:16px 18px;border-radius:8px;background:rgba(109,93,255,.1);border:1px solid rgba(109,93,255,.35)}
</style></head><body>
<h1>{{.Title}}</h1>
{{if .Submitted}}
<div class="done"><strong>Thanks!</strong> Your submission was received.</div>
{{else}}
<form method="post">
{{range .Fields}}
<label for="{{.}}">{{label .}}</label>
{{if isArea .}}<textarea id="{{.}}" name="{{.}}"></textarea>{{else}}<input id="{{.}}" name="{{.}}" type="{{inputType .}}">{{end}}
{{end}}
<button type="submit">Submit</button>
</form>
{{end}}
</body></html>`))

// formInputType picks an HTML input type from the field name so phones
// surface the right keyboard. Best-effort and purely cosmetic.
func formInputType(field string) string {
	switch strings.ToLower(field) {
	case "email":
		return "email"
	case "phone", "tel", "telephone":
		return "tel"
	case "url", "website":
		return "url"
	default:
		return "text"
	}
}
