// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine/webapi"
)

// The admin API behind Admin → Web APIs.
//
// Every handler is scoped to p.Tenant and never takes a tenant from the
// request, exactly as the MCP-server handlers are: an org administering "its"
// catalogs is administering the rows its session is for, and there is no path
// here that names another org's.

// webAPIRow is the list's wire shape.
//
// Unlike mcpServerRow there is no credential to keep out of it — this feature
// stores none — so the type is the row, plus what this process knows about it
// live.
type webAPIRow struct {
	// Name is the id flows reference. Read-only after creation: the client
	// shows it, it does not offer to change it.
	Name  string `json:"name"`
	Label string `json:"label"`
	// Description is the org's blurb about the service, shown on its page under
	// Apps. Round-tripped so the form can edit it.
	Description string `json:"description,omitempty"`
	BaseURL     string `json:"base_url"`
	Integration string `json:"integration,omitempty"`
	AuthKind    string `json:"auth_kind"`
	// AuthHeader is the header name for auth_kind "header". The NAME is not a
	// secret; the value it carries is, and that value is not stored here at all
	// — it lives in the org's connection for this integration.
	AuthHeader   string             `json:"auth_header,omitempty"`
	Operations   []webapi.Operation `json:"operations"`
	TimeoutMS    int                `json:"timeout_ms,omitempty"`
	MaxBodyBytes int                `json:"max_body_bytes,omitempty"`
	Enabled      bool               `json:"enabled"`
	// Logo is the service's brand mark as a data: URI, guessed from its favicon
	// when the catalog was saved. Sent so the admin page can show the SAME image
	// the palette and the Apps page will use — a mark that turned out to be the
	// wrong one is worth seeing where it can be corrected, not only downstream.
	Logo string `json:"logo,omitempty"`
	// LogoMode is where that mark came from: "auto" (the service's favicon),
	// "custom" (an image an admin chose) or "none" (the plain glyph, on
	// purpose). Always sent, because the form has to open on the right choice
	// and "absent" would read as auto for a catalog that had said none.
	LogoMode string `json:"logo_mode"`
	// RunnerTags, when present, means this catalog's calls are made from one of
	// the org's own machines rather than from the daemon — the only way to reach
	// a service with no public address. Always sent (never omitted) so the form
	// opens on the right state: "absent" would read as "direct" for a catalog
	// that is in fact on a runner.
	RunnerTags []string `json:"runner_tags"`
	// SpecURL is where an imported catalog's operations came from. Sent so the
	// page can offer "refresh from the spec" without asking for the address
	// again; empty means hand-built (or imported by paste).
	SpecURL string `json:"spec_url,omitempty"`
	// Registered is the live fact: this catalog is in THIS process's engine
	// catalog right now. There is no "connected" for a described API — nothing
	// was dialed — so this is the honest equivalent, and the page must not
	// present it as a health check.
	Registered bool `json:"registered"`
	// StepIDs are the step ids this catalog contributes, so the page can show
	// what was gained rather than only a count.
	StepIDs   []string  `json:"step_ids,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type webAPIRequest struct {
	// Label is the display name and, on a create, what the id is derived from.
	// Blank on an edit keeps the stored one.
	Label string `json:"label,omitempty"`
	// Name sets the id explicitly. The UI never sends it on a create — it sends
	// Label and lets the daemon derive one. Kept for an API caller that wants to
	// choose the id its flows will reference.
	Name string `json:"name,omitempty"`
	// Description is a pointer because blank is a real value here — an org
	// clearing the paragraph — so "not sent" cannot be spelled as "".
	Description  *string            `json:"description,omitempty"`
	BaseURL      string             `json:"base_url"`
	Integration  string             `json:"integration,omitempty"`
	AuthKind     string             `json:"auth_kind,omitempty"`
	AuthHeader   string             `json:"auth_header,omitempty"`
	Operations   []webapi.Operation `json:"operations"`
	TimeoutMS    int                `json:"timeout_ms,omitempty"`
	MaxBodyBytes int                `json:"max_body_bytes,omitempty"`
	// Enabled is a pointer so "not sent" is distinguishable from "false": a PUT
	// that omits it must not silently disable a working catalog.
	Enabled *bool `json:"enabled,omitempty"`
	// Logo and LogoMode are pointers for the same reason, and it matters more
	// here: a save that omitted them and was read as "auto, no image" would
	// throw away an uploaded mark on every edit of anything else.
	//
	// Logo is a data: URI — the image itself, not a link to one, because the
	// app's CSP does not load third-party images. Sending it without a mode
	// means "use this".
	Logo     *string `json:"logo,omitempty"`
	LogoMode *string `json:"logo_mode,omitempty"`
	// RunnerTags is a pointer-to-slice for the same reason Enabled is a pointer:
	// a PUT that omits it must not move a catalog off its runner and back onto a
	// direct call the network will refuse. Sending [] is how you turn it off.
	RunnerTags *[]string `json:"runner_tags,omitempty"`
	// SpecURL is a pointer for the same reason: an edit of anything else must
	// not make an imported catalog forget where it came from.
	SpecURL *string `json:"spec_url,omitempty"`
}

func (h *HTTPGateway) webAPIsConfigured(rw http.ResponseWriter) bool {
	if h.WebAPIs == nil || h.WebAPIs.Store == nil || h.WebAPIs.Catalog == nil {
		writeJSONError(rw, http.StatusNotImplemented, "web APIs are not configured on this deployment")
		return false
	}
	return true
}

// decodeWebAPIBody bounds the body.
//
// Larger than the MCP limit and for a real reason: this body carries a
// DESCRIPTION of an API — operations, arguments, per-argument schemas — not a
// handful of short strings. 1 MiB fits a hand-built catalog many times over
// while still bounding what an authenticated route will buffer. The operation
// and argument caps in webapis.go are the semantic bound; this is only the
// memory one.
func decodeWebAPIBody(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

// webAPIRowFor renders one stored row with this process's live view merged in.
func (h *HTTPGateway) webAPIRowFor(w WebAPI, live map[string][]string) webAPIRow {
	ids, registered := live[w.Name]
	ops := w.Operations
	if ops == nil {
		// Never null on the wire: the client renders a list, and a null would
		// make every consumer implement the empty case itself.
		ops = []webapi.Operation{}
	}
	return webAPIRow{
		Name:         w.Name,
		Label:        w.DisplayName(),
		Description:  w.Description,
		BaseURL:      w.BaseURL,
		Integration:  w.Integration,
		AuthKind:     string(w.AuthKind),
		AuthHeader:   w.AuthHeader,
		Operations:   ops,
		TimeoutMS:    w.TimeoutMS,
		MaxBodyBytes: w.MaxBodyBytes,
		Enabled:      w.Enabled,
		Logo:         w.Logo,
		LogoMode:     string(w.logoMode()),
		RunnerTags:   w.RunnerTags,
		SpecURL:      w.SpecURL,
		Registered:   registered,
		StepIDs:      ids,
		LastError:    w.LastError,
		CreatedBy:    w.CreatedBy,
		CreatedAt:    w.CreatedAt,
		UpdatedAt:    w.UpdatedAt,
	}
}

// liveWebAPIs indexes what this process currently has registered for the
// tenant, by catalog name.
func (h *HTTPGateway) liveWebAPIs(tenant string) map[string][]string {
	out := map[string][]string{}
	for _, st := range h.WebAPIs.Catalog.CatalogsFor(tenant) {
		out[st.Name] = st.StepIDs
	}
	return out
}

func (h *HTTPGateway) listWebAPIs(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.webAPIsConfigured(rw) {
		return
	}
	rows, err := h.WebAPIs.List(r.Context(), p.Tenant)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	live := h.liveWebAPIs(p.Tenant)
	out := make([]webAPIRow, 0, len(rows))
	for _, w := range rows {
		out = append(out, h.webAPIRowFor(w, live))
	}
	writeJSON(rw, http.StatusOK, map[string]any{"web_apis": out})
}

// saveWebAPI handles both create (POST) and edit (PUT).
//
// One handler for the same reason saveMCPServer is one: a catalog is identified
// by its name, and saving under an existing name replaces that configuration.
// Two paths would have to agree on validation and on registration.
func (h *HTTPGateway) saveWebAPI(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.webAPIsConfigured(rw) {
		return
	}
	var req webAPIRequest
	if err := decodeWebAPIBody(r, &req); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "malformed request body")
		return
	}
	// On PUT the name is in the path and the body's copy is ignored, so a
	// mismatched body cannot rename (and thereby re-key) a catalog behind the
	// caller's back. The LABEL is free to change on the same request: nothing
	// references it.
	if pathName := r.PathValue("name"); pathName != "" {
		req.Name = pathName
	}
	in := WebAPIInput{
		Label:        req.Label,
		Name:         req.Name,
		Description:  req.Description,
		BaseURL:      req.BaseURL,
		Integration:  req.Integration,
		AuthKind:     webapi.AuthKind(req.AuthKind),
		AuthHeader:   req.AuthHeader,
		Operations:   req.Operations,
		TimeoutMS:    req.TimeoutMS,
		MaxBodyBytes: req.MaxBodyBytes,
		Enabled:      true,
		Logo:         req.Logo,
	}
	if req.Enabled != nil {
		in.Enabled = *req.Enabled
	}
	if req.RunnerTags != nil {
		in.RunnerTags = *req.RunnerTags
		// A non-nil empty slice must survive as non-nil: it is the caller
		// saying "stop using a runner", which is not the same as not saying.
		if in.RunnerTags == nil {
			in.RunnerTags = []string{}
		}
	}
	if req.SpecURL != nil {
		in.SpecURL = req.SpecURL
	}
	if req.LogoMode != nil {
		// Passed through unvalidated on purpose: the service owns the vocabulary
		// and refuses an unknown source with a message naming the three, which
		// is one place to change rather than two to keep in step.
		mode := WebAPILogoMode(*req.LogoMode)
		in.LogoMode = &mode
	}

	saved, err := h.WebAPIs.Save(r.Context(), p.Tenant, p.Subject, in)
	if err != nil {
		if errors.Is(err, ErrWebAPIsUnconfigured) {
			writeJSONError(rw, http.StatusNotImplemented, err.Error())
			return
		}
		// Everything Save rejects is the caller's input, named and explained —
		// including what the engine's descriptor validation rejected, whose
		// messages are written for exactly this display.
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	// Audited with the address, not with anything secret: an admin pointing the
	// daemon at a new service is the act a reviewer needs to see later.
	h.audit(r.Context(), p, "web_api.save", saved.Name, saved.BaseURL)
	writeJSON(rw, http.StatusOK, h.webAPIRowFor(saved, h.liveWebAPIs(p.Tenant)))
}

// webAPIUsage answers "what breaks if I delete this".
//
// Same shape and same reasoning as the MCP servers' usage route: its own
// endpoint because it loads every graph in the org, which is fine once when a
// delete confirmation opens and wasteful on every render of the list.
func (h *HTTPGateway) webAPIUsage(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.webAPIsConfigured(rw) {
		return
	}
	name := r.PathValue("name")
	// Report usage only for a catalog that exists, so a typo'd name cannot be
	// answered with a confident "nothing uses this".
	if _, err := h.WebAPIs.Store.Get(r.Context(), p.Tenant, name); err != nil {
		if errors.Is(err, ErrWebAPINotFound) {
			writeJSONError(rw, http.StatusNotFound, "no web API named "+name)
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if h.svc == nil {
		writeJSONError(rw, http.StatusNotImplemented, "flow storage is not configured on this deployment")
		return
	}
	usage, err := h.svc.FlowsUsingWebAPI(r.Context(), p, p.Tenant, name)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, usage)
}

func (h *HTTPGateway) deleteWebAPI(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.webAPIsConfigured(rw) {
		return
	}
	name := r.PathValue("name")
	if err := h.WebAPIs.Delete(r.Context(), p.Tenant, name); err != nil {
		if errors.Is(err, ErrWebAPINotFound) {
			writeJSONError(rw, http.StatusNotFound, "no web API named "+name)
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	// Audited with what it broke, not just what was removed — same as the MCP
	// servers' delete.
	detail := ""
	if h.svc != nil {
		if usage, uerr := h.svc.FlowsUsingWebAPI(r.Context(), p, p.Tenant, name); uerr == nil && usage.InUse() {
			detail = fmt.Sprintf("in use by %d flow(s)", len(usage.Flows)+usage.Hidden)
		}
	}
	h.audit(r.Context(), p, "web_api.delete", name, detail)
	writeJSON(rw, http.StatusOK, map[string]any{"deleted": name})
}

// ── Importing an OpenAPI spec ───────────────────────────────────────────────

// webAPISpecRequest asks for a spec to be read. Exactly one source is used:
// a URL the daemon fetches through the guarded caller, or a document the admin
// pasted.
type webAPISpecRequest struct {
	// URL is fetched through the same guarded Doer a step's call uses — the
	// SSRF guard, the egress allowlist and the response cap all apply, because
	// this address is tenant-supplied like any other.
	URL string `json:"url,omitempty"`
	// Spec is a pasted document, for an API whose spec is not reachable from
	// the daemon (behind a VPN, on a laptop, generated locally).
	Spec string `json:"spec,omitempty"`
	// Against, when set, names an existing catalog to diff the parsed
	// operations against — the refresh case. Empty means a first import.
	Against string `json:"against,omitempty"`
}

// webAPISpecResponse is what the picker renders.
type webAPISpecResponse struct {
	Title       string             `json:"title,omitempty"`
	Description string             `json:"description,omitempty"`
	BaseURL     string             `json:"base_url,omitempty"`
	Operations  []webapi.Operation `json:"operations"`
	Tags        []string           `json:"tags,omitempty"`
	// OperationTags maps operation id to its spec tags, so the picker can select
	// by tag. Kept beside the operations rather than on them: Operation is a
	// STORED type and tags are useful only before saving.
	OperationTags map[string][]string    `json:"operation_tags,omitempty"`
	Warnings      []webapi.ImportWarning `json:"warnings,omitempty"`
	// Diff is present only for a refresh (`against` was sent and matched). The
	// page uses it to show what a re-import would add, change and REMOVE, and
	// to require confirmation for the removals.
	Diff *webapi.RefreshDiff `json:"diff,omitempty"`
	// Overflow says the spec offers more operations than one catalog may hold.
	// Reported rather than truncated: which ones to keep is the admin's choice,
	// and silently taking the first sixty would be making it for them.
	Overflow bool `json:"overflow,omitempty"`
	// Max is the cap, so the page can say "pick at most N" without hardcoding it.
	Max int `json:"max"`
}

// parseWebAPISpec reads a spec and reports what could be imported from it.
//
// It stores NOTHING. Import is the ordinary save that follows, carrying the
// operations the admin actually picked — which is what makes "import
// operations, never register a spec" true in the wiring and not only in the
// documentation.
func (h *HTTPGateway) parseWebAPISpec(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.webAPIsConfigured(rw) {
		return
	}
	var req webAPISpecRequest
	if err := decodeWebAPIBody(r, &req); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "malformed request body")
		return
	}
	url, pasted := strings.TrimSpace(req.URL), strings.TrimSpace(req.Spec)
	if (url == "") == (pasted == "") {
		writeJSONError(rw, http.StatusBadRequest, "send either a spec address or a pasted document, not both and not neither")
		return
	}

	var (
		parsed webapi.SpecImport
		err    error
	)
	if url != "" {
		parsed, err = webapi.FetchSpec(r.Context(), url)
	} else {
		parsed, err = webapi.ParseSpec([]byte(pasted))
	}
	if err != nil {
		// The parser's messages are written to be shown to the admin who pasted
		// the document — "this is a Swagger 2.0 document…" — so they are
		// forwarded rather than replaced with a generic 400.
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}

	resp := webAPISpecResponse{
		Title:         parsed.Title,
		Description:   parsed.Description,
		BaseURL:       parsed.BaseURL,
		Operations:    parsed.Operations,
		Tags:          parsed.Tags,
		OperationTags: parsed.OperationTags,
		Warnings:      parsed.Warnings,
		Max:           maxWebAPIOperations,
		Overflow:      len(parsed.Operations) > maxWebAPIOperations,
	}

	if name := strings.TrimSpace(req.Against); name != "" {
		existing, err := h.WebAPIs.Store.Get(r.Context(), p.Tenant, name)
		if err != nil && !errors.Is(err, ErrWebAPINotFound) {
			writeJSONError(rw, http.StatusInternalServerError, "could not read the stored catalog")
			return
		}
		if err == nil {
			diff := webapi.DiffOperations(existing.Name, existing.Operations, parsed.Operations)
			resp.Diff = &diff
		}
	}
	writeJSON(rw, http.StatusOK, resp)
}
