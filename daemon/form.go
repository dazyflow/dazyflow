// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/maillang"
)

var defaultFormFields = []string{"name", "email", "message"}

// A PUBLIC page: no bearer token, so possession of the URL is the capability and
// everything here must assume an anonymous, hostile caller.
func (w *WebhookListener) handleForm(rw http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/form/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		http.Error(rw, "expected /form/<tenant>/<workspace>/<graph-id>", http.StatusBadRequest)
		return
	}
	tenant, workspace, graphID := parts[0], parts[1], parts[2]

	store, err := w.svc.Workspaces.Open(tenant, workspace)
	if err != nil {
		renderFormUnavailable(rw, "")
		return
	}
	g, err := store.LoadPublished(graphID)
	if err != nil {
		renderFormUnavailable(rw, "")
		return
	}
	if g.Disabled {
		renderFormUnavailable(rw, g.Language)
		return
	}
	fields, title, ok := publicFormConfig(g)
	if !ok {
		renderFormUnavailable(rw, g.Language)
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

	view := formView{
		Title:    title,
		Fields:   fields,
		Honeypot: honeypotField(fields),
		Lang:     maillang.Primary(g.Language),
		M:        maillang.For(g.Language),
	}

	switch r.Method {
	case http.MethodGet:
		renderForm(rw, http.StatusOK, view)
	case http.MethodPost:
		r.Body = http.MaxBytesReader(rw, r.Body, 1<<20)

		posted, err := parseFormBody(r)
		if err != nil {
			// A 200 here would look like a successful submission to the visitor.
			if errors.Is(err, errFormUnsupportedMedia) {
				http.Error(rw,
					"this form accepts application/x-www-form-urlencoded, multipart/form-data or a flat application/json object; "+
						"to send other content types, use this flow's /trigger endpoint with its secret key",
					http.StatusUnsupportedMediaType)
				return
			}
			// Nothing to re-fill the form with, so the page cannot be re-rendered.
			view.Error = view.M.FormErrorRetry
			renderForm(rw, http.StatusBadRequest, view)
			return
		}

		// A filled honeypot means a bot completed every field, so answer as if sent.
		if hp := honeypotField(fields); hp != "" && strings.TrimSpace(posted.Get(hp)) != "" {
			w.logger.Printf("form %s/%s/%s: honeypot filled, submission dropped", tenant, workspace, graphID)
			view.Submitted = true
			renderForm(rw, http.StatusOK, view)
			return
		}
		posted.Del(honeypotName)

		view.Values = declaredFormValues(fields, posted)
		values := collectFormValues(fields, posted)
		seed := buildFormSeed(fields, values)
		seeds := map[string]core.Result{}
		for _, n := range g.Nodes {
			if n.Module == core.FormInputModule && !triggerNodeDisabled(n) {
				seeds[n.ID] = seed
			}
		}
		if len(seeds) == 0 {
			// Unreachable: publicFormConfig already rejected a flow without a form.
			view.Error = view.M.FormErrorClosed
			renderForm(rw, http.StatusBadRequest, view)
			return
		}
		principal := SystemPrincipal("dazyflow-form", g.Tenant, g.Workspace)
		// A trigger endpoint like /trigger, so it carries the same chain-depth guard: a
		// flow whose step posts back to its own form would otherwise run for ever.
		runID, err := w.svc.SubmitGraphOpts(context.WithoutCancel(r.Context()), principal, g, SubmitOpts{
			Seeds:        seeds,
			TriggerDepth: inboundTriggerDepth(r),
		})
		if err != nil {
			w.logger.Printf("form submit %s/%s/%s: %v", tenant, workspace, graphID, err)
			view.Error = view.M.FormErrorRetry
			status := http.StatusInternalServerError
			if ownerMustFix(err) {
				view.Error = view.M.FormErrorClosed
				status = http.StatusServiceUnavailable
				// Somebody just typed this, so the refusal has to be re-rendered with it.
				capCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
				_, stored := w.svc.recordRefusedDelivery(capCtx, g, seeds,
					refusalCode(err), refusalMessage(err))
				cancel()
				if !stored {
					w.logger.Printf("form submit %s/%s/%s: submission NOT stored",
						tenant, workspace, graphID)
				}
			}
			renderForm(rw, status, view)
			return
		}
		w.logger.Printf("form %s/%s/%s → %s", tenant, workspace, graphID, runID)
		view.Submitted = true
		renderForm(rw, http.StatusOK, view)
	default:
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Distinguishes a visitor's mistake from a misconfiguration only the owner can fix.
func ownerMustFix(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, core.ErrPlanLimit) ||
		errors.Is(err, core.ErrOrgSuspended) ||
		errors.Is(err, core.ErrGraphTooLarge) ||
		strings.Contains(err.Error(), "invalid graph")
}

var errFormUnsupportedMedia = errors.New("unsupported media type")

// A hidden field; a real visitor never fills it.
const honeypotName = "dz_confirm_url"

// Empty when the owner declared a field of the same name themselves.
func honeypotField(declared []string) string {
	for _, f := range declared {
		if strings.EqualFold(strings.TrimSpace(f), honeypotName) {
			return ""
		}
	}
	return honeypotName
}

// Accepts both urlencoded and JSON, so a page and an API caller both work.
func parseFormBody(r *http.Request) (url.Values, error) {
	mediaType := r.Header.Get("Content-Type")
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))

	switch mediaType {
	case "", "application/x-www-form-urlencoded", "multipart/form-data":
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		return r.PostForm, nil

	case "application/json":
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if len(strings.TrimSpace(string(raw))) == 0 {
			return url.Values{}, nil
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, err
		}
		out := make(url.Values, len(obj))
		for k, v := range obj {
			out.Set(k, jsonScalarToString(v))
		}
		return out, nil
	}
	return nil, errFormUnsupportedMedia
}

func jsonScalarToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		if b, err := json.Marshal(t); err == nil {
			return string(b)
		}
		return fmt.Sprint(t)
	}
}

// Caps a SUBMISSION as well as the render, so a field past it could never be
// filled in anyway.
const maxFormFields = core.MaxHostedFormFields

// Seeds the trigger's body port.
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

func publicFormConfig(g core.Graph) (fields []string, title string, ok bool) {
	for _, n := range g.Nodes {
		if n.Module != core.FormInputModule {
			continue
		}
		if triggerNodeDisabled(n) {
			continue
		}
		t, _ := n.Params["form_title"].(string)
		if len(t) > core.MaxHostedFormTitleLen {
			t = t[:core.MaxHostedFormTitleLen]
		}
		fields := formStringSlice(n.Params["form_fields"])
		// Capping the COUNT bounded the wrong half: a name has no natural length, and
		// the page emits each one four times on every anonymous GET.
		fields = slices.DeleteFunc(fields, func(f string) bool {
			return len(f) > core.MaxHostedFormFieldLen
		})
		if len(fields) > maxFormFields {
			// Every declared field renders on every anonymous GET, so an uncapped list amplifies.
			fields = fields[:maxFormFields]
		}
		return fields, t, true
	}
	return nil, "", false
}

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

// Only the fields the form RENDERS, so an extra POST key cannot inject one.
func declaredFormValues(declared []string, posted url.Values) map[string]string {
	out := make(map[string]string, len(declared))
	for _, f := range declared {
		out[f] = posted.Get(f)
	}
	return out
}

// The column order the submission carries downstream.
func formSeedHeaders(declared []string, values map[string]any) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, f := range declared {
		if _, ok := values[f]; !ok {
			continue // declared but past the field cap
		}
		if _, dup := seen[f]; dup {
			continue // owner typed the same name twice
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	extra := make([]string, 0, len(values))
	for k := range values {
		if _, ok := seen[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

// Mirrors buildWebhookSeed's shape, so downstream steps see one contract.
func buildFormSeed(declared []string, values map[string]any) core.Result {
	return core.Result{
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"body": {
				MIME:    "application/json",
				Inline:  values,
				Headers: formSeedHeaders(declared, values),
			},
		},
	}
}

type formView struct {
	Title       string
	Fields      []string
	Submitted   bool
	Honeypot    string
	Lang        string
	M           maillang.Messages
	Error       string
	Values      map[string]string
	Unavailable bool
}

// Owner-supplied, so it is escaped on render.
func humanizeField(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// Must not disclose whether the flow exists.
func renderFormUnavailable(rw http.ResponseWriter, lang string) {
	m := maillang.For(lang)
	renderForm(rw, http.StatusNotFound, formView{
		Title:       m.FormGoneTitle,
		Lang:        maillang.Primary(lang),
		M:           m,
		Unavailable: true,
	})
}

func renderForm(rw http.ResponseWriter, status int, v formView) {
	if v.Values == nil {
		v.Values = map[string]string{}
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Deliberately frameable: embedding it on the org's own site is the point.
	rw.Header().Set("Content-Security-Policy", "frame-ancestors *")
	rw.WriteHeader(status)
	if err := formTemplate.Execute(rw, v); err != nil {
		fmt.Fprintf(rw, "<!-- render error: %v -->", err)
	}
}

// Self-contained and dependency-free. html/template escapes every interpolation,
// which is what keeps an owner-supplied label or a visitor's echoed value from
// becoming markup.
var formTemplate = template.Must(template.New("form").Funcs(template.FuncMap{
	"label":     humanizeField,
	"inputType": formInputType,
	"isArea":    isLongAnswerField,
}).Parse(`<!doctype html>
<html lang="{{.Lang}}"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
/* Palette mirrors web/src/theme.css so the page a visitor lands on is
   recognisably the same product as the editor that published it. Hardcoded
   because this page ships without the app's stylesheet. */
:root{
  color-scheme:light dark;
  --bg:#f6f7fb; --card:#ffffff; --ink:#1b2233; --soft:#39415a; --muted:#5b6577;
  --line:#e4e8f0; --field:#ffffff; --accent:#6d28d9; --accent-ink:#ffffff;
  --ring:rgba(109,40,217,.24); --err:#dc2626;
  --lift:0 1px 2px rgba(27,34,51,.05),0 10px 30px rgba(27,34,51,.07);
}
@media(prefers-color-scheme:dark){:root{
  --bg:#070513; --card:#140d30; --ink:#e9ecf3; --soft:#d2caff; --muted:#9e9abb;
  --line:#383451; --field:#0f0a24; --accent:#9f83fe; --accent-ink:#140d30;
  --ring:rgba(159,131,254,.3); --err:#ff6464;
  --lift:0 1px 2px rgba(8,2,30,.5),0 12px 34px rgba(8,2,30,.5);
}}
*{box-sizing:border-box}
body{
  margin:0; padding:40px 16px;
  font-family:system-ui,-apple-system,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif;
  color:var(--ink); background:var(--bg);
  -webkit-font-smoothing:antialiased;
}
/* Centred horizontally but NOT vertically: the page is deliberately frameable,
   and viewport-height centring clips it inside a short iframe. */
.card{
  max-width:34rem; margin:0 auto; padding:36px 32px;
  background:var(--card); border:1px solid var(--line);
  border-radius:18px; box-shadow:var(--lift);
}
h1{margin:0 0 28px; font-size:1.5rem; font-weight:600; letter-spacing:-.015em; line-height:1.25}
.field+.field{margin-top:20px}
label{display:block; margin-bottom:6px; font-size:.875rem; font-weight:500; color:var(--soft)}
input,textarea{
  width:100%; padding:11px 13px;
  border:1px solid var(--line); border-radius:10px;
  /* 16px: anything smaller makes mobile Safari zoom the page on focus. */
  font-family:inherit; font-size:16px; font-weight:400; line-height:1.5;
  color:var(--ink); background:var(--field);
  transition:border-color 160ms cubic-bezier(.22,.7,.3,1),box-shadow 160ms cubic-bezier(.22,.7,.3,1);
}
input::placeholder,textarea::placeholder{color:var(--muted)}
input:hover,textarea:hover{border-color:var(--muted)}
input:focus,textarea:focus{outline:0; border-color:var(--accent); box-shadow:0 0 0 3px var(--ring)}
textarea{min-height:132px; resize:vertical}
button{
  margin-top:28px; padding:12px 22px;
  border:0; border-radius:10px;
  background:var(--accent); color:var(--accent-ink);
  font-family:inherit; font-size:.9375rem; font-weight:600;
  cursor:pointer;
  transition:filter 160ms cubic-bezier(.22,.7,.3,1),transform 160ms cubic-bezier(.22,.7,.3,1);
}
button:hover{filter:brightness(1.08)}
button:active{transform:translateY(1px)}
button:focus-visible{outline:2px solid var(--accent); outline-offset:3px}
.note{
  margin:0 0 24px; padding:14px 16px;
  border:1px solid color-mix(in srgb,var(--err) 40%,transparent);
  border-radius:10px; background:color-mix(in srgb,var(--err) 9%,transparent);
  font-size:.9375rem; line-height:1.5;
}
.note strong{display:block; margin-bottom:2px}
/* Left-aligned like the heading: a centred block under a left-aligned title
   puts two alignment systems on one small card. */
.thanks{display:flex; gap:14px; align-items:flex-start}
.tick{
  flex:0 0 auto; width:38px; height:38px;
  display:flex; align-items:center; justify-content:center;
  border-radius:50%; background:color-mix(in srgb,var(--accent) 14%,transparent);
  color:var(--accent);
}
.thanks-title{margin:0 0 4px; font-size:1.0625rem; font-weight:600; line-height:1.4}
.thanks-body{margin:0; color:var(--muted); font-size:.9375rem; line-height:1.55}
/* The confirmation replaces the form, so the heading needs less air under it. */
.card-done h1{margin-bottom:22px}
.hp{position:absolute; left:-9999px; width:1px; height:1px; overflow:hidden}
@media(max-width:480px){
  body{padding:20px 12px}
  .card{padding:24px 20px; border-radius:14px}
  h1{font-size:1.3125rem; margin-bottom:22px}
  button{width:100%}
}
@media(prefers-reduced-motion:reduce){
  input,textarea,button{transition:none}
  button:active{transform:none}
}
</style></head><body>
<main class="card{{if .Submitted}} card-done{{end}}">
<h1>{{.Title}}</h1>
{{if .Unavailable}}
<p class="note" role="alert">{{.M.FormGoneBody}}</p>
{{else if .Submitted}}
<div class="thanks" role="status">
<div class="tick" aria-hidden="true"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.75" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12.5l5 5L20 6.5"/></svg></div>
<div>
<p class="thanks-title">{{.M.FormThanksTitle}}</p>
<p class="thanks-body">{{.M.FormThanksBody}}</p>
</div>
</div>
{{else}}
{{if .Error}}<p class="note" role="alert"><strong>{{.M.FormErrorTitle}}</strong>{{.Error}}</p>{{end}}
<form method="post">
{{if .Honeypot}}<div class="hp" aria-hidden="true"><label for="{{.Honeypot}}">{{.M.FormHoneypot}}</label><input id="{{.Honeypot}}" name="{{.Honeypot}}" type="text" tabindex="-1" autocomplete="off"></div>{{end}}
{{range .Fields}}
<div class="field">
<label for="{{.}}">{{label .}}</label>
{{if isArea .}}<textarea id="{{.}}" name="{{.}}">{{index $.Values .}}</textarea>{{else}}<input id="{{.}}" name="{{.}}" type="{{inputType .}}" value="{{index $.Values .}}">{{end}}
</div>
{{end}}
<button type="submit">{{.M.FormSubmit}}</button>
</form>
{{end}}
</main>
</body></html>`))

var longAnswerWords = []string{
	"message", "comment", "feedback", "question", "enquiry", "inquiry",
	"describe", "description", "details", "reason", "note", "notes",
	"review", "testimonial", "story", "about", "why", "what you",
	"meddelande", "kommentar", "fråga", "beskriv", "beskrivning",
	"anteckning", "omdöme", "berätta", "varför",
}

func isLongAnswerField(f string) bool {
	s := strings.ToLower(strings.TrimSpace(f))
	if s == "" {
		return false
	}
	for _, w := range longAnswerWords {
		if strings.Contains(s, w) {
			return true
		}
	}
	return strings.Contains(s, "?") || len(strings.Fields(s)) >= 5
}

var (
	emailWords = []string{"email", "epost", "mejl"}
	phoneWords = []string{"phone", "telefon", "mobil", "tfn"}
	urlWords   = []string{"website", "webbplats", "webbsida", "hemsida"}
)

func formInputType(field string) string {
	s := normalizeFieldWord(field)
	if s == "" {
		return "text"
	}
	kinds := []string{}
	if containsAnyHint(s, emailWords) {
		kinds = append(kinds, "email")
	}
	if containsAnyHint(s, phoneWords) {
		kinds = append(kinds, "tel")
	}
	if s == "url" || containsAnyHint(s, urlWords) {
		kinds = append(kinds, "url")
	}
	// A name that reads as two kinds at once must not be typed as either.
	if len(kinds) != 1 {
		return "text"
	}
	return kinds[0]
}

func normalizeFieldWord(field string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '_', '.', '/', '\t':
			return -1
		}
		return unicode.ToLower(r)
	}, field)
}

func containsAnyHint(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}
