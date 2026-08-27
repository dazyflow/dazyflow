// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	Name        string `json:"name"`
	Label       string `json:"label"`
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
	Name         string             `json:"name,omitempty"`
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
