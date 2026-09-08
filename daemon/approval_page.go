// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// The page the signed approval link opens.
//
// The link is emailed to a person and rendered as a button, so opening it is a
// GET — and for a long time the only thing registered here was POST, which
// meant the one-click approval the mail promises answered a raw
// "method_not_allowed" JSON body. This file is the missing half: GET renders
// the question with an Approve and a Reject button, and those buttons post
// back to the same URL.
//
// GET stays free of side effects, deliberately. Mail scanners and link
// previewers fetch URLs out of messages before a human sees them, so an
// approval that happened on GET would be decided by a virus scanner.

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/maillang"
)

type approvalView struct {
	Lang     string
	M        maillang.Messages
	Prompt   string
	Action   string
	Done     bool
	Decision string
	Gone     bool
	Already  bool
}

// handleApprovalPage serves GET /approve/<run>/<node>. The token is verified
// before anything is rendered, and every failure — bad signature, expired,
// unknown run — renders the same "not valid" page, so the URL cannot be used
// to discover which runs exist.
func (a *ApprovalListener) handleApprovalPage(rw http.ResponseWriter, r *http.Request) {
	graphRunID, nodeID, ok := parseApprovalPath(rw, r)
	if !ok {
		return
	}
	if !a.tokenOK(r, graphRunID, nodeID) {
		renderApproval(rw, http.StatusUnauthorized, approvalView{
			Lang: "en", M: maillang.English, Gone: true,
		})
		return
	}

	lang, prompt, status := a.approvalPageState(r, graphRunID, nodeID)
	m := maillang.For(lang)
	view := approvalView{
		Lang:   maillang.Primary(lang),
		M:      m,
		Prompt: prompt,
		Action: r.URL.RequestURI(),
	}
	switch status {
	case core.JobStatusAwaiting:
		renderApproval(rw, http.StatusOK, view)
	case "":
		view.Gone = true
		renderApproval(rw, http.StatusNotFound, view)
	default:
		view.Already = true
		renderApproval(rw, http.StatusOK, view)
	}
}

// approvalPageState reads the flow language, the step's question and the
// parked record's status. An unreadable record answers an empty status, which
// the caller renders as a dead link rather than a form that cannot work.
func (a *ApprovalListener) approvalPageState(r *http.Request, graphRunID, nodeID string) (lang, prompt string, status core.JobStatus) {
	if a.svc == nil || a.svc.Jobs == nil {
		return "", "", ""
	}
	rec, err := a.svc.Jobs.Get(r.Context(), NodeJobID(graphRunID, nodeID))
	if err != nil {
		return "", "", ""
	}
	if rec.Result != nil {
		if ref, ok := rec.Result.Output["prompt"]; ok {
			prompt, _ = ref.Inline.(string)
		}
	}
	if graphRec, err := a.svc.Jobs.Get(r.Context(), graphRunID); err == nil && len(graphRec.GraphPayload) > 0 {
		var g core.Graph
		if json.Unmarshal(graphRec.GraphPayload, &g) == nil {
			lang = g.Language
		}
	}
	return lang, prompt, rec.Status
}

// wantsHTML reports whether the caller is a browser rather than a script.
//
// It decides what a POST answers with: the buttons on the page are a form
// submit and their sender needs a page back, while the scripts that have been
// posting this URL all along need the JSON they already parse. A form submit
// from any browser sends "text/html" in Accept; curl and Go's client send
// nothing or */*.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func renderApproval(rw http.ResponseWriter, status int, v approvalView) {
	if v.M.ApprovalApprove == "" {
		v.M = maillang.English
	}
	if v.Lang == "" {
		v.Lang = "en"
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	rw.WriteHeader(status)
	_ = approvalTemplate.Execute(rw, v)
}

// approvalTemplate is a self-contained, dependency-free page — no SPA bundle,
// no fonts, no script. It has to work in whatever browser a mail client hands
// the link to, including one that has never loaded this deployment before.
//
// html/template escapes every interpolation, so the flow's own question cannot
// inject markup into the page an approver is looking at.
var approvalTemplate = template.Must(template.New("approve").Parse(`<!doctype html>
<html lang="{{.Lang}}"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex,nofollow">
<title>{{if .Gone}}{{.M.ApprovalGoneTitle}}{{else if .Already}}{{.M.ApprovalAlreadyTitle}}{{else if .Done}}{{if eq .Decision "reject"}}{{.M.ApprovalDoneRejected}}{{else}}{{.M.ApprovalDoneApproved}}{{end}}{{else}}{{.M.ApprovalPageTitle}}{{end}}</title>
<style>
:root{color-scheme:light dark}
body{font-family:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;max-width:480px;margin:0 auto;padding:48px 20px;color:#1a1730;background:#fbfaff}
@media(prefers-color-scheme:dark){body{color:#e6e2f5;background:#0f0d1c}textarea{background:#1c1930;color:#e6e2f5;border-color:#332d52}}
h1{font-size:22px;margin:0 0 12px}
p{line-height:1.5}
.q{margin:20px 0;padding:16px 18px;border-radius:8px;background:rgba(109,93,255,.1);border:1px solid rgba(109,93,255,.35);font-size:17px;font-weight:600}
label{display:block;margin:20px 0 4px;font-weight:600;font-size:14px}
textarea{width:100%;padding:10px 12px;border:1px solid #cfc7ea;border-radius:8px;font:inherit;box-sizing:border-box;min-height:80px;resize:vertical}
.row{display:flex;gap:10px;margin-top:24px;flex-wrap:wrap}
button{padding:12px 20px;border:0;border-radius:8px;font:inherit;font-weight:600;cursor:pointer}
.ok{background:#2f9e5f;color:#fff}.ok:hover{background:#28874f}
.no{background:transparent;color:#c94444;border:1px solid rgba(201,68,68,.5)}.no:hover{background:rgba(201,68,68,.1)}
.done{padding:16px 18px;border-radius:8px;background:rgba(109,93,255,.1);border:1px solid rgba(109,93,255,.35)}
</style></head><body>
{{if .Gone}}
  <h1>{{.M.ApprovalGoneTitle}}</h1>
  <p>{{.M.ApprovalGoneBody}}</p>
{{else if .Already}}
  <h1>{{.M.ApprovalAlreadyTitle}}</h1>
  <p>{{.M.ApprovalAlreadyBody}}</p>
{{else if .Done}}
  <div class="done">
    <h1>{{if eq .Decision "reject"}}{{.M.ApprovalDoneRejected}}{{else}}{{.M.ApprovalDoneApproved}}{{end}}</h1>
    <p>{{.M.ApprovalDoneBody}}</p>
  </div>
{{else}}
  <h1>{{.M.ApprovalPageTitle}}</h1>
  <p>{{.M.ApprovalPageIntro}}</p>
  {{if .Prompt}}<div class="q">{{.Prompt}}</div>{{end}}
  <form method="post" action="{{.Action}}">
    <label for="c">{{.M.ApprovalCommentLabel}}</label>
    <textarea id="c" name="comment"></textarea>
    <div class="row">
      <button class="ok" type="submit" name="decision" value="approve">{{.M.ApprovalApprove}}</button>
      <button class="no" type="submit" name="decision" value="reject">{{.M.ApprovalReject}}</button>
    </div>
  </form>
{{end}}
</body></html>
`))
