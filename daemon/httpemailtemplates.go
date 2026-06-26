package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/emailtmpl"
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
func (h *HTTPGateway) emailTemplateGate(rw http.ResponseWriter, p core.Principal, write bool) bool {
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
	if err := validSecretName(name); err != nil {
		return err
	}
	if strings.Contains(name, ":") {
		return fmt.Errorf("template name may not contain ':'")
	}
	return nil
}

// putEmailTemplate creates/replaces an org template. Idempotent.
func (h *HTTPGateway) putEmailTemplate(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.emailTemplateGate(rw, p, true) {
		return
	}
	name := r.PathValue("name")
	if err := validEmailTemplateName(name); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxEmailTemplateBytes)
	var body putEmailTemplateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
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
	// Stored ID equals the name for org templates; Name is the display label
	// (falls back to the name).
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

// listEmailTemplates returns the global built-ins followed by this org's
// templates. HTML is included for both so the editor and live preview need no
// second round-trip; built-ins are flagged read-only.
func (h *HTTPGateway) listEmailTemplates(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
func (h *HTTPGateway) previewEmailTemplate(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.emailTemplateGate(rw, p, false) {
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxEmailTemplateBytes)
	var req struct {
		// HTML is a shell to preview directly (the management editor's unsaved
		// edits). Takes precedence over ID.
		HTML string `json:"html"`
		// ID resolves a saved/built-in template's shell — the drop's "Preview
		// email" button passes the selected template id here.
		ID string `json:"id"`
		// Body/Subject are the actual message content to wrap (the drop's typed
		// body). Body falls back to sample content when empty so an empty-body
		// preview still shows the layout.
		Body    string `json:"body"`
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}

	const sampleBody = `<h1 style="margin:0 0 14px;font-size:22px;">Hello there 👋</h1>` +
		`<p style="margin:0 0 14px;">This is a preview of your email template with some sample body content so you can see how the layout wraps a real message.</p>` +
		`<p style="margin:0;">Best,<br>The team</p>`

	// Resolve the shell: explicit HTML wins; else resolve the template id the
	// same way the runtime does (built-ins ∪ this tenant's templates); else a
	// bare passthrough so a no-template preview still shows the body.
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
		body = sampleBody
	}
	rendered, err := emailtmpl.WrapBody(shell, body, req.Subject, logo)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("render template: %v", err))
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"html": rendered})
}

// previewLogo returns the org logo for the preview, or "" — same source the
// runtime provider uses, so the preview matches a real send.
func (h *HTTPGateway) previewLogo(r *http.Request, p core.Principal) string {
	if h.Profiles == nil {
		return ""
	}
	prof, err := h.Profiles.GetOrgProfile(r.Context(), p.Tenant)
	if err != nil {
		return ""
	}
	return emailtmpl.NormalizeLogo(prof.Icon)
}

// deleteEmailTemplate removes an org template. Idempotent. Built-in IDs are
// global and read-only — deleting one is rejected.
func (h *HTTPGateway) deleteEmailTemplate(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
func (h *HTTPGateway) emailTemplateStorageNames(ctx context.Context, tenant string) map[string]string {
	all, err := h.EncryptedSecrets.List(ctx, tenant)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, n := range all {
		// Org templates are exactly "emailtmpl.<name>" — exclude any
		// flow-prefixed entries (templates have no flow tier, but be defensive).
		if strings.HasPrefix(n, secretEmailTmplPrefix) && !strings.HasPrefix(n, secretFlowPrefix) {
			out[strings.TrimPrefix(n, secretEmailTmplPrefix)] = n
		}
	}
	return out
}
