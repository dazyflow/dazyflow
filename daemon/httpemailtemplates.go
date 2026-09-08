// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/internal/emailtmpl"
	"github.com/dazyflow/dazyflow/internal/smtputil"
)

// Email templates are reusable HTML layout shells the email-sending drops wrap
// a body in (see core.EmailTemplate). CRUD lives here; org templates are
// stored in the encrypted secret store under the reserved "emailtmpl."
// namespace at organization scope (tenant-wide, no flow tier). Unlike a
// secret, a template's HTML is not sensitive, so GET returns it.
//
//	GET    /api/v1/email-templates         → built-ins ∪ this org's templates
//	PUT    /api/v1/email-templates/{name}  → create/replace an org template
//	DELETE /api/v1/email-templates/{name}  → remove an org template
//
// Built-in templates (ID "builtin:…") are global and read-only: they always
// appear in the list and cannot be written or deleted.

const maxEmailTemplateBytes = 256 * 1024 // 256 KiB — HTML shells are larger than secrets

const emailTemplateSampleBody = `<h1 style="margin:0 0 14px;font-size:22px;">Hello there 👋</h1>` +
	`<p style="margin:0 0 14px;">This is a preview of your email template with some sample body content so you can see how the layout wraps a real message.</p>` +
	`<p style="margin:0;">Best,<br>The team</p>`

type putEmailTemplateBody struct {
	Name string `json:"name"`
	HTML string `json:"html"`
}

type emailTemplateView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	HTML     string `json:"html"`
	Builtin  bool   `json:"builtin"`
	ReadOnly bool   `json:"readOnly"`
}

// emailTemplateGate is the shared preamble for the template CRUD handlers:
// encrypted store present, tenant-bound principal, and authorization. The
// template library is org-wide branding with a live-reference blast radius (a
// change affects every flow that uses it), so MANAGEMENT (create/edit/delete)
// is admin-only (organization:admin). READS (list/preview) only need
// secret:read, so any editor can pick and preview a template when building a
// flow. Templates have no flow tier, so there is no scope query param. Returns
// ok=false (after writing the error) when the handler should stop.
func (h *secretsAPI) emailTemplateGate(rw http.ResponseWriter, p core.Principal, write bool) bool {
	if h.EncryptedSecrets == nil {
		writeJSONError(rw, http.StatusNotImplemented, "encrypted secret store is not configured")
		return false
	}
	if p.Tenant == "" {
		writeJSONError(rw, http.StatusForbidden, "principal has no tenant")
		return false
	}
	if write {
		if !core.CanAdminOrg(p) {
			writeJSONError(rw, http.StatusForbidden, "managing email templates requires organization admin")
			return false
		}
		return true
	}
	if err := core.Require(p, core.PermSecretRead); err != nil {
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return false
	}
	return true
}

// validEmailTemplateName accepts the same characters as a secret name but
// forbids ":" (validSecretName already excludes it) so an org template name
// can never collide with a "builtin:" ID. Kept explicit in case the secret
// rule ever loosens.
func validEmailTemplateName(name string) error {
	if err := core.ValidSecretName(name); err != nil {
		return err
	}
	if strings.Contains(name, ":") {
		return fmt.Errorf("template name may not contain ':'")
	}
	return nil
}

func (h *secretsAPI) putEmailTemplate(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.emailTemplateGate(rw, p, true) {
		return
	}
	name := r.PathValue("name")
	if err := validEmailTemplateName(name); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxEmailTemplateBytes)
	body, ok := decodeRequestJSON[putEmailTemplateBody](rw, r)
	if !ok {
		return
	}
	html := strings.TrimSpace(body.HTML)
	if html == "" {
		writeJSONError(rw, http.StatusBadRequest, "html must not be empty")
		return
	}
	if !emailtmpl.HasBodyPlaceholder(html) {
		writeJSONError(rw, http.StatusBadRequest, "html must be a valid template containing the {{.Body}} placeholder")
		return
	}
	display := strings.TrimSpace(body.Name)
	if display == "" {
		display = name
	}
	def := core.EmailTemplate{ID: name, Name: display, HTML: html}
	raw, err := json.Marshal(def)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("encode template: %v", err))
		return
	}
	if err := h.EncryptedSecrets.PutScoped(r.Context(), p.Tenant, "", ScopeTenant, secretEmailTmplPrefix+name, string(raw)); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("store template: %v", err))
		return
	}
	h.audit(r.Context(), p, "email_template.put", name, "")
	rw.WriteHeader(http.StatusNoContent)
}

func (h *secretsAPI) listEmailTemplates(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.emailTemplateGate(rw, p, false) {
		return
	}
	out := make([]emailTemplateView, 0)
	for _, t := range emailtmpl.BuiltinTemplates() {
		out = append(out, emailTemplateView{ID: t.ID, Name: t.Name, HTML: t.HTML, Builtin: true, ReadOnly: true})
	}

	org := make([]emailTemplateView, 0)
	for name, storage := range h.emailTemplateStorageNames(r.Context(), p.Tenant) {
		raw, err := h.EncryptedSecrets.GetExact(r.Context(), p.Tenant, storage)
		if err != nil {
			continue // racing delete, or a stray prefix collision
		}
		var def core.EmailTemplate
		if err := json.Unmarshal([]byte(raw), &def); err != nil {
			continue
		}
		display := def.Name
		if display == "" {
			display = name
		}
		org = append(org, emailTemplateView{ID: name, Name: display, HTML: def.HTML})
	}
	sort.Slice(org, func(i, j int) bool { return org[i].Name < org[j].Name })
	out = append(out, org...)
	writeJSON(rw, http.StatusOK, map[string]any{"templates": out})
}

// previewEmailTemplate renders a shell with a sample body so the editor can
// show a faithful preview — the shells use html/template control actions
// ({{if .Logo}}, {{safeURL .Logo}}) a browser can't execute, so a client-side
// string replace would mangle them. Read-only (secret:read); takes the HTML in
// the body so it previews unsaved edits.
func (h *secretsAPI) previewEmailTemplate(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.emailTemplateGate(rw, p, false) {
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxEmailTemplateBytes)
	req, ok := decodeRequestJSON[struct {
		HTML    string `json:"html"`
		ID      string `json:"id"`
		Body    string `json:"body"`
		Subject string `json:"subject"`
	}](rw, r)
	if !ok {
		return
	}

	shell := strings.TrimSpace(req.HTML)
	logo := h.previewLogo(r, p)
	if shell == "" && strings.TrimSpace(req.ID) != "" {
		provider := &EmailTemplateProvider{Secrets: h.EncryptedSecrets, Profiles: h.Profiles}
		resolved, orgLogo, ok, err := provider.TemplateHTML(r.Context(), p.Tenant, strings.TrimSpace(req.ID))
		if err != nil {
			writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("resolve template: %v", err))
			return
		}
		if !ok {
			writeJSONError(rw, http.StatusNotFound, "template not found")
			return
		}
		shell, logo = resolved, orgLogo
	}
	if shell == "" {
		shell = "{{.Body}}"
	}

	body := req.Body
	if strings.TrimSpace(body) == "" {
		body = emailTemplateSampleBody
	}
	rendered, err := emailtmpl.WrapBody(shell, body, req.Subject, logo)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("render template: %v", err))
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"html": rendered})
}

func (h *secretsAPI) previewLogo(r *http.Request, p core.Principal) string {
	if h.Profiles == nil {
		return ""
	}
	prof, err := h.Profiles.GetOrgProfile(r.Context(), p.Tenant)
	if err != nil {
		return ""
	}
	return emailtmpl.NormalizeLogo(prof.Icon)
}

type sendTestEmailBody struct {
	To      string `json:"to"`
	HTML    string `json:"html"`
	ID      string `json:"id"`
	Subject string `json:"subject"`
}

// sendTestEmail renders a template shell around the sample body and delivers it
// to one recipient through the TENANT's own Email (SMTP) connection — the same
// transport the email_send drop uses at run time — so an editor can confirm a
// template lands correctly in a real inbox before wiring it into a flow.
//
//	POST /api/v1/email-templates/send-test  {to?, html?, id?, subject?}
//
// Reading the decrypted SMTP credentials and actually sending makes this a
// secret:write action (like the connection "Test" button), a step up from the
// secret:read needed to list/preview. The recipient defaults to the caller, so
// the usual click is a zero-input "send the template to me".
func (h *secretsAPI) sendTestEmail(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.EncryptedSecrets == nil {
		writeJSONError(rw, http.StatusNotImplemented, "encrypted secret store is not configured")
		return
	}
	if p.Tenant == "" {
		writeJSONError(rw, http.StatusForbidden, "principal has no tenant")
		return
	}
	if err := core.Require(p, core.PermSecretWrite); err != nil {
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return
	}

	r.Body = http.MaxBytesReader(rw, r.Body, maxEmailTemplateBytes)
	body, ok := decodeRequestJSON[sendTestEmailBody](rw, r)
	if !ok {
		return
	}
	to := strings.TrimSpace(body.To)
	if to == "" {
		to = p.Subject // mirror the editor's prefill: default to the caller
	}
	if !strings.Contains(to, "@") || strings.ContainsAny(to, " \r\n") {
		writeJSONError(rw, http.StatusBadRequest, "a valid recipient address is required")
		return
	}

	integration, fields, err := h.connectionFieldsForSlug(r.Context(), p, "email")
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if len(fields) == 0 {
		writeJSONError(rw, http.StatusNotImplemented, "the Email integration is unavailable on this install")
		return
	}
	conn, _ := h.candidateConnection(r.Context(), p.Tenant, integration, fields, nil)
	if label := missingRequired(fields, conn); label != "" {
		writeJSONError(rw, http.StatusConflict,
			fmt.Sprintf("connect Email first — %s is not set on the Email integration page", label))
		return
	}

	host := strings.TrimSpace(conn["host"])
	from := strings.TrimSpace(conn["from"])
	if from == "" {
		from = strings.TrimSpace(conn["username"]) // SMTP login is usually the sender
	}
	fromHeader, fromAddr := smtputil.SplitSender(from)
	port := 587
	if s := strings.TrimSpace(conn["port"]); s != "" {
		n, perr := strconv.Atoi(s)
		if perr != nil || n <= 0 {
			writeJSONError(rw, http.StatusBadRequest, "the configured port is not a number — fix it on the Email integration page")
			return
		}
		port = n
	}
	mode := strings.TrimSpace(conn["tls"])
	if mode == "" {
		mode = "starttls"
	}

	shell := strings.TrimSpace(body.HTML)
	logo := h.previewLogo(r, p)
	if shell == "" && strings.TrimSpace(body.ID) != "" {
		provider := &EmailTemplateProvider{Secrets: h.EncryptedSecrets, Profiles: h.Profiles}
		resolved, orgLogo, ok, terr := provider.TemplateHTML(r.Context(), p.Tenant, strings.TrimSpace(body.ID))
		if terr != nil {
			writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("resolve template: %v", terr))
			return
		}
		if !ok {
			writeJSONError(rw, http.StatusNotFound, "template not found")
			return
		}
		shell, logo = resolved, orgLogo
	}
	if shell == "" {
		shell = "{{.Body}}"
	}

	subject := strings.TrimSpace(body.Subject)
	if subject == "" {
		subject = "Dazyflow email template test"
	}
	htmlBody, err := emailtmpl.WrapBody(shell, emailTemplateSampleBody, subject, logo)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("render template: %v", err))
		return
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	// The SMTP host is tenant-supplied (and may have been set via the raw
	// secrets path, which skips the connect-time verifier's guard), so refuse
	// private/loopback targets unless the operator opted into private egress —
	// the same SSRF guard the email_send drop applies.
	if err := hfnet.CheckDialHost(addr); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}

	// Same rule as the drop and the connect-time check: no login at all is a
	// valid relay setup, half a login is a mistake — never a silent
	// unauthenticated send (smtputil.Auth).
	auth, aerr := smtputil.Auth(host, conn["username"], conn["password"])
	if aerr != nil {
		writeJSONError(rw, http.StatusBadRequest, "mail server login is incomplete: "+aerr.Error())
		return
	}

	const textAlt = "This is a test of your Dazyflow email template. " +
		"Open it in an HTML-capable client to see the rendered layout."
	msg, err := multipartMessage(fromHeader, fromAddr, to, subject, textAlt, htmlBody)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("build message: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), connectionVerifyTimeout)
	defer cancel()
	if err := smtputil.Send(ctx, addr, host, mode, auth, fromAddr, []string{to}, msg); err != nil {
		h.audit(r.Context(), p, "email_template.test_send", to, "error="+err.Error())
		writeJSONError(rw, http.StatusBadGateway, fmt.Sprintf("send failed: %v", err))
		return
	}
	h.audit(r.Context(), p, "email_template.test_send", to, "ok")
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "to": to, "from": fromHeader})
}

func (h *secretsAPI) deleteEmailTemplate(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.emailTemplateGate(rw, p, true) {
		return
	}
	name := r.PathValue("name")
	if emailtmpl.IsBuiltinID(name) {
		writeJSONError(rw, http.StatusConflict, "built-in templates are read-only")
		return
	}
	if err := validEmailTemplateName(name); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.EncryptedSecrets.DeleteScoped(r.Context(), p.Tenant, "", ScopeTenant, secretEmailTmplPrefix+name); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("delete template: %v", err))
		return
	}
	h.audit(r.Context(), p, "email_template.delete", name, "")
	rw.WriteHeader(http.StatusNoContent)
}

// emailTemplateStorageNames maps org template name → its full storage name.
// ListScoped hides the reserved "emailtmpl." prefix, so this filters the raw
// name list itself (mirrors resourceStorageNames at tenant scope).
func (h *secretsAPI) emailTemplateStorageNames(ctx context.Context, tenant string) map[string]string {
	all, err := h.EncryptedSecrets.List(ctx, tenant)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, n := range all {
		if strings.HasPrefix(n, secretEmailTmplPrefix) && !strings.HasPrefix(n, secretFlowPrefix) {
			out[strings.TrimPrefix(n, secretEmailTmplPrefix)] = n
		}
	}
	return out
}
