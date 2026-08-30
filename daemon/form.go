// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/maillang"
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
		renderFormUnavailable(rw, "")
		return
	}
	// Hosted form submissions run the published revision, matching the
	// webhook listener — an unpublished flow's form is not live.
	g, err := store.LoadPublished(graphID)
	if err != nil {
		// The common case by far: the owner copied the form link out of the
		// editor and shared it before pressing Publish. They get told about
		// that in the editor; whoever they sent it to gets this page.
		renderFormUnavailable(rw, "")
		return
	}
	if g.Disabled {
		// Symmetric with the webhook: a paused flow's form is off.
		// Use 404 (not 403) to match the rest of this handler's
		// "don't reveal whether the graph exists" stance.
		renderFormUnavailable(rw, g.Language)
		return
	}
	fields, title, ok := publicFormConfig(g)
	if !ok {
		// Either no webhook_input node or it hasn't opted into a public form.
		// Don't reveal which — the same page as every other miss keeps
		// non-public graphs invisible.
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

	// The flow's language, resolved once: every page this handler can serve —
	// the form, the confirmation, either error — must be in the same one.
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
		// Cap the body before we materialize it — the global 200 MiB limit is
		// far too loose for a public, unauthenticated form (mirrors the 1 MiB
		// webhook cap).
		r.Body = http.MaxBytesReader(rw, r.Body, 1<<20)

		posted, err := parseFormBody(r)
		if err != nil {
			// An encoding this endpoint can't read. Answering 200 here (which
			// is what a bare ParseForm did for, say, a JSON body — it leaves
			// PostForm empty without erroring) started a run and appended a
			// row with every column blank, so the caller was told "Thanks!"
			// while the owner silently collected junk. Refuse instead, and say
			// which encodings work.
			if errors.Is(err, errFormUnsupportedMedia) {
				http.Error(rw,
					"this form accepts application/x-www-form-urlencoded, multipart/form-data or a flat application/json object; "+
						"to send other content types, use this flow's /trigger endpoint with its secret key",
					http.StatusUnsupportedMediaType)
				return
			}
			// Unreadable, malformed, or over the 1 MiB cap. Nothing to re-fill
			// (the body never parsed), but the visitor still gets a page
			// rather than Times New Roman on white.
			view.Error = view.M.FormErrorRetry
			renderForm(rw, http.StatusBadRequest, view)
			return
		}

		// A filled honeypot means a bot walked the DOM and completed every
		// input it found. Answer exactly as a success would — a bot that can
		// tell it was refused just tries again without the field — but run
		// nothing and store nothing.
		if hp := honeypotField(fields); hp != "" && strings.TrimSpace(posted.Get(hp)) != "" {
			w.logger.Printf("form %s/%s/%s: honeypot filled, submission dropped", tenant, workspace, graphID)
			view.Submitted = true
			renderForm(rw, http.StatusOK, view)
			return
		}
		posted.Del(honeypotName)

		view.Values = declaredFormValues(fields, posted)
		values := collectFormValues(fields, posted)
		seed := buildFormSeed(values)
		seeds := map[string]core.Result{}
		for _, n := range g.Nodes {
			if n.Module == webhookInputModuleID && !triggerNodeDisabled(n) {
				seeds[n.ID] = seed
			}
		}
		if len(seeds) == 0 {
			// Unreachable by construction: publicFormConfig above already
			// found a live (non-paused) webhook_input, and this loop's
			// predicate is the weaker one. Kept as a guard, answering the
			// same way the owner-side refusals below do rather than with a
			// bare status, so a future edit that breaks the invariant still
			// shows a visitor a page.
			view.Error = view.M.FormErrorClosed
			renderForm(rw, http.StatusBadRequest, view)
			return
		}
		principal := SystemPrincipal("dazyflow-form", g.Tenant, g.Workspace)
		runID, err := w.svc.SubmitGraphWithSeed(r.Context(), principal, g, seeds)
		if err != nil {
			w.logger.Printf("form submit %s/%s/%s: %v", tenant, workspace, graphID, err)
			// "Try again" is only honest for a transient failure. A refusal the
			// OWNER has to act on — over the plan's run allowance, a suspended
			// org, a graph that no longer validates — will refuse the next
			// attempt too, and the visitor has no way to know that. Tell them
			// to reach the owner another way instead of leaving them retyping.
			view.Error = view.M.FormErrorRetry
			status := http.StatusInternalServerError
			if ownerMustFix(err) {
				view.Error = view.M.FormErrorClosed
				status = http.StatusServiceUnavailable
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

// ownerMustFix reports whether a submission failure is one the flow's OWNER
// has to resolve, rather than something a visitor's second attempt could get
// past. The sentinels are the submission gates in SubmitGraphOpts; an invalid
// graph joins them because it will keep failing validation until the flow is
// edited. Anything unrecognised counts as transient — telling someone to try
// again when they can't is a smaller wrong than telling them not to bother
// when they could.
func ownerMustFix(err error) bool {
	return errors.Is(err, core.ErrPlanLimit) ||
		errors.Is(err, core.ErrOrgSuspended) ||
		errors.Is(err, core.ErrGraphTooLarge) ||
		strings.Contains(err.Error(), "invalid graph")
}

// errFormUnsupportedMedia marks a POST whose Content-Type this endpoint
// cannot read, so the caller gets 415 rather than a blank-row "success".
var errFormUnsupportedMedia = errors.New("unsupported media type")

// honeypotName is the hidden field the rendered form carries. Bots that
// fill every input they find give themselves away by completing it; real
// visitors never see it. The name is deliberately plausible-but-namespaced:
// plausible so a naive bot fills it, namespaced so it can't collide with a
// field an owner actually declared.
const honeypotName = "dz_confirm_url"

// honeypotField returns the honeypot's name, or "" when an owner has
// declared a field of the same name (in which case the field is theirs and
// the trap is off — silently discarding a real answer would be far worse
// than missing a bot).
func honeypotField(declared []string) string {
	for _, f := range declared {
		if strings.EqualFold(strings.TrimSpace(f), honeypotName) {
			return ""
		}
	}
	return honeypotName
}

// parseFormBody reads a hosted-form POST into url.Values, accepting the
// encodings a form submission can plausibly arrive in:
//
//   - application/x-www-form-urlencoded → the browser's own encoding
//   - multipart/form-data               → same, for forms with a file input
//   - application/json                  → a FLAT object of scalars, for
//     anyone hand-rolling a form against this URL rather than embedding ours
//   - no body / no Content-Type         → treated as urlencoded (empty)
//
// Anything else is errFormUnsupportedMedia. That refusal is the point: the
// previous code called r.ParseForm() unconditionally, and ParseForm does not
// report an error for a Content-Type it doesn't handle — it simply leaves
// PostForm empty. A JSON caller therefore got 200, a real run, and a row with
// every column blank.
//
// Nested JSON (an object or array under a key) is flattened to its compact
// JSON text rather than rejected: the columns are TEXT anyway, and keeping
// the value is more useful to the owner than refusing the whole submission.
func parseFormBody(r *http.Request) (url.Values, error) {
	mediaType := r.Header.Get("Content-Type")
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}
	// Media types are case-insensitive (RFC 9110 §8.3.1) — "Application/JSON"
	// must read the same as the lowercase form.
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
			// Valid JSON that isn't an object (an array, a bare string) has no
			// field names to map onto form fields, and malformed JSON has
			// nothing at all. Either way the caller needs to know.
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

// jsonScalarToString renders one JSON value as the text a form field would
// have carried. Numbers keep their literal form (json.Number-style, via
// strconv) so an id like 10000000000000001 doesn't come out as 1e+16, and
// composites keep their JSON text so nothing is silently dropped.
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
		// A paused trigger step has no public form. Rendering the form and
		// then refusing (or worse, accepting into a run that skips the very
		// node meant to receive it) is a crueller answer than not offering it.
		if triggerNodeDisabled(n) {
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

// declaredFormValues picks out just the fields the form actually RENDERS, as
// strings, so a failed submission can hand them back in the inputs. It is
// deliberately not collectFormValues: that one keeps undeclared extras (utm_*
// and friends) because the flow wants them, whereas re-filling a field the page
// never drew would silently drop them anyway.
func declaredFormValues(declared []string, posted url.Values) map[string]string {
	out := make(map[string]string, len(declared))
	for _, f := range declared {
		out[f] = posted.Get(f)
	}
	return out
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
	// Honeypot is the name of the hidden anti-bot field to render, or "" to
	// render none (an owner declared a field of that name, so it's theirs).
	Honeypot string
	// Lang is the BCP-47 primary subtag for <html lang>, and M the copy in the
	// same language. Both come from the flow's own Language: a form is the flow
	// speaking to a visitor, exactly as an approval email is (see
	// internal/maillang), and the visitor has no account to hold a preference.
	Lang string
	M    maillang.Messages
	// Error, when set, renders a banner above the form instead of the
	// confirmation. Values re-fills the fields, so a failure never costs the
	// visitor what they typed — the previous behaviour was a plain-text
	// http.Error page, which lost it and offered no way back.
	Error  string
	Values map[string]string
	// Unavailable renders the "there is no form here" page: the notice alone,
	// with no form beneath it to fill in. Set by renderFormUnavailable for
	// every 404 this handler can produce.
	Unavailable bool
}

// humanizeField turns a field name into the label the form shows.
//
// It deliberately does LESS than the frontend's humanize, because it is fed
// something different. The frontend humanizes manifest param keys — machine
// identifiers like "first_row_headers" that nobody typed. These names come out
// of the owner's own "Form fields" box, are read by their customers, and so
// are edited toward what they want shown rather than away from it:
//
//   - Underscores become spaces. "first_name" is a shape people paste in from
//     somewhere else, and "_" never appears in a label anyone wrote by hand.
//   - HYPHENS ARE LEFT ALONE. They are letters in ordinary words — "E-post",
//     "e-mail", "follow-up", "self-employed" — and stripping them published
//     "E post" on a Swedish company's contact form.
//   - The first letter is capitalized, so "what you love about our tea" reads
//     as a label without the owner having to think about it. Anything already
//     capitalized is untouched.
func humanizeField(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	if s == "" {
		return s
	}
	// Decode the first RUNE. Slicing s[:1] took the first BYTE, which turned
	// every label starting with a non-ASCII letter — "Ärende", "Önskemål",
	// "Écrire" — into invalid UTF-8 on a page shown to the public.
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		// Not valid UTF-8 to begin with; hand it back rather than corrupt it
		// further. html/template still escapes whatever this is.
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// renderFormUnavailable answers a request for a form that isn't there to
// answer — the workspace, the published revision, or the public-form opt-in is
// missing, or the flow is paused. Every one of those renders the SAME page:
// naming which would tell a stranger whether a given flow exists, and this
// handler's whole stance is that it shouldn't.
//
// It is HTML rather than the API's JSON error envelope because this is the one
// URL in the product an owner hands to their own customers. A link copied out
// of the editor and shared before publishing used to show them
// {"error":{"code":"not_found","message":"Not Found"}} on a bare white page.
//
// lang is the flow's, when we got far enough to load one; empty falls back to
// English. We deliberately don't load the draft just to translate this page —
// that would put a store read on an unauthenticated endpoint's miss path.
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
		// The template indexes Values for every field; a nil map would work in
		// html/template but not in any reader's head.
		v.Values = map[string]string{}
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Allow the form to be iframed onto any site — the "Put this form on
	// my own website" embed snippet relies on it. Set frame-ancestors
	// explicitly (rather than leaning on the absence of X-Frame-Options)
	// so the intent is deliberate. Clickjacking risk is low: the form is
	// secret-less and submits only public, owner-declared fields.
	rw.Header().Set("Content-Security-Policy", "frame-ancestors *")
	rw.WriteHeader(status)
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
//
// Its own words — the button, the confirmation, either error — come from the
// flow's language rather than being written in English here, and `lang` is set
// from the same resolution so the attribute cannot contradict the copy. This
// is the only page in the product a stranger sees, so it was also the only one
// that still spoke English to a Swedish visitor.
//
// On a failed submission it renders the form again, banner on top and the
// posted values back in the fields, instead of the plain-text http.Error page
// that used to lose what the visitor had typed.
var formTemplate = template.Must(template.New("form").Funcs(template.FuncMap{
	"label":     humanizeField,
	"inputType": formInputType,
	"isArea":    isLongAnswerField,
}).Parse(`<!doctype html>
<html lang="{{.Lang}}"><head>
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
.err{padding:14px 16px;border-radius:8px;background:rgba(201,68,68,.1);border:1px solid rgba(201,68,68,.4);margin-bottom:4px}
.hp{position:absolute;left:-9999px;width:1px;height:1px;overflow:hidden}
</style></head><body>
<h1>{{.Title}}</h1>
{{if .Unavailable}}
<div class="err" role="alert">{{.M.FormGoneBody}}</div>
{{else if .Submitted}}
<div class="done"><strong>{{.M.FormThanksTitle}}</strong> {{.M.FormThanksBody}}</div>
{{else}}
{{if .Error}}<div class="err" role="alert"><strong>{{.M.FormErrorTitle}}</strong> {{.Error}}</div>{{end}}
<form method="post">
{{if .Honeypot}}<div class="hp" aria-hidden="true"><label for="{{.Honeypot}}">{{.M.FormHoneypot}}</label><input id="{{.Honeypot}}" name="{{.Honeypot}}" type="text" tabindex="-1" autocomplete="off"></div>{{end}}
{{range .Fields}}
<label for="{{.}}">{{label .}}</label>
{{if isArea .}}<textarea id="{{.}}" name="{{.}}">{{index $.Values .}}</textarea>{{else}}<input id="{{.}}" name="{{.}}" type="{{inputType .}}" value="{{index $.Values .}}">{{end}}
{{end}}
<button type="submit">{{.M.FormSubmit}}</button>
</form>
{{end}}
</body></html>`))

// longAnswerWords are the field-name hints that mean "this answer is prose",
// and so deserves a textarea rather than a one-line input. Kept in both
// English and Swedish because the hosted form is the one page in the product
// a stranger sees, and owners name their fields in their own language.
var longAnswerWords = []string{
	"message", "comment", "feedback", "question", "enquiry", "inquiry",
	"describe", "description", "details", "reason", "note", "notes",
	"review", "testimonial", "story", "about", "why", "what you",
	"meddelande", "kommentar", "fråga", "beskriv", "beskrivning",
	"anteckning", "omdöme", "berätta", "varför",
}

// isLongAnswerField reports whether a declared field should render as a
// textarea. The old rule matched only the literal word "message", so a field
// an owner actually named — "What you like about us", "Your feedback",
// "Tell us why" — got a single-line box for what is obviously a paragraph.
//
// Two signals, either of which is enough: a recognisable long-answer word, or
// a label long enough that it is plainly a question rather than a column name
// ("What did you think of your visit?" vs "Name").
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
	// A question mark is an unambiguous tell, and a field name of five or more
	// words is a sentence, not a label.
	return strings.Contains(s, "?") || len(strings.Fields(s)) >= 5
}

// The field-name hints behind the keyboard promise, kept in both languages the
// product ships for the same reason longAnswerWords above is: owners name their
// fields in their own language, and this is the page their customers fill in on
// a phone.
//
// They are matched against a name with its separators removed, so "E-post",
// "e_post" and "E post" all reduce to "epost" and one entry covers them.
var (
	emailWords = []string{"email", "epost", "mejl"}
	phoneWords = []string{"phone", "telefon", "mobil", "tfn"}
	urlWords   = []string{"website", "webbplats", "webbsida", "hemsida"}
)

// formInputType picks an HTML input type from the field name so phones surface
// the right keyboard. Best-effort: getting it wrong costs a keyboard layout,
// so the rule leans on recognising a word rather than on being exhaustive.
//
// The old rule compared the whole name against a handful of English words, so
// only a field named exactly "email" was ever recognised. Every natural
// phrasing missed — "Email address", "Your email", and every non-English name
// the docs' "Email and Phone get the matching keyboard" promise implied.
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
	// A name that reads as two kinds at once — "Email or phone", "Phone/web" —
	// is a field for either, and type=email would make the browser REJECT the
	// other one on submit. A plain text box accepts both.
	if len(kinds) != 1 {
		return "text"
	}
	return kinds[0]
}

// normalizeFieldWord lowercases a field name and drops the separators people
// put between words, so one hint matches every way of writing the same name.
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
