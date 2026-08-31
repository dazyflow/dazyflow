// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine/mcp"
)

// The admin API behind Admin → MCP servers.
//
// Every handler is scoped to p.Tenant and never takes a tenant from the
// request. An org administering "its" servers is administering exactly the
// rows its session is for; there is no path here that names another org's.

// mcpServerRow is the list's wire shape.
//
// No token, and no field for one. The credential is write-only by
// construction — the store's read columns exclude it — so this type cannot
// carry one back to a browser even if a future handler forgets to think
// about it.
type mcpServerRow struct {
	// Name is the id flows reference. Read-only after creation: the client
	// shows it, it does not offer to change it.
	Name string `json:"name"`
	// Label is the display name. Always populated on the wire — a row saved
	// before labels existed reports its id here, so a client never has to
	// implement the fallback itself.
	Label    string `json:"label"`
	URL      string `json:"url"`
	AuthKind string `json:"auth_kind"`
	// AuthHeader is the header name for auth_kind "header". The NAME is not a
	// secret; the value it carries is.
	AuthHeader string `json:"auth_header,omitempty"`
	// HasToken tells the edit form whether a credential is stored, so it can
	// offer "replace" rather than demanding the token be retyped on every
	// save.
	HasToken bool `json:"has_token"`
	Enabled  bool `json:"enabled"`
	// Connected is the live fact: this row has a working session in THIS
	// process right now. A server whose steps are being described from cache
	// is NOT connected. Distinct from last_connected, which is the last time
	// any replica managed it.
	Connected bool `json:"connected"`
	// ToolIDs are the step ids this server contributes, so the page can show
	// what was actually gained rather than only a count.
	ToolIDs []string `json:"tool_ids,omitempty"`
	// Instructions is what the server said about itself at handshake, verbatim.
	// Live-only, like Connected and ToolIDs: it comes from the connection this
	// process holds, so a row connected on another replica reports none.
	//
	// Third-party text on an admin page. It is rendered as text, never as
	// markup, and nothing here or downstream acts on it.
	Instructions string `json:"instructions,omitempty"`
	// ProtocolVersion is the MCP revision this connection settled on, which
	// is not necessarily the one we asked for. Live-only, and here because it
	// is the answer to "the server has tools but none of them have icons" —
	// icons arrived in 2025-11-25, and a server on an older revision sends
	// none.
	ProtocolVersion string    `json:"protocol_version,omitempty"`
	ToolCount       int       `json:"tool_count"`
	LastError       string    `json:"last_error,omitempty"`
	LastConnected   time.Time `json:"last_connected,omitempty"`
	CreatedBy       string    `json:"created_by,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type mcpServerRequest struct {
	// Label is the display name and, on a create, what the id is derived from.
	// Blank on an edit keeps the stored one.
	Label string `json:"label,omitempty"`
	// Name sets the id explicitly. The UI never sends it on a create — it
	// sends Label and lets the daemon derive one. Kept for an API caller that
	// wants to choose the id its flows will reference.
	Name       string `json:"name,omitempty"`
	URL        string `json:"url"`
	AuthKind   string `json:"auth_kind"`
	AuthHeader string `json:"auth_header,omitempty"`
	// Token empty on an edit means "keep the stored one".
	Token string `json:"token,omitempty"`
	// Enabled is a pointer so "not sent" is distinguishable from "false": a
	// PUT that omits it must not silently disable a working server.
	Enabled *bool `json:"enabled,omitempty"`
}

func (h *HTTPGateway) mcpServersConfigured(rw http.ResponseWriter) bool {
	if h.MCPServers == nil || h.MCPServers.Store == nil || h.MCPServers.Catalog == nil {
		writeJSONError(rw, http.StatusNotImplemented, "MCP servers are not configured on this deployment")
		return false
	}
	return true
}

// decodeMCPBody bounds the body. A server registration is a handful of short
// strings; without a cap this is an unauthenticated-shaped memory sink behind
// an authenticated route.
func decodeMCPBody(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(v)
}

// mcpRowFor renders one stored row with this process's live view merged in.
func (h *HTTPGateway) mcpRowFor(s MCPServer, live map[string]mcp.ServerStatus) mcpServerRow {
	st, registered := live[s.Name]
	// Registered is not the same as connected any more: a server whose
	// handshake failed stays in the catalog DESCRIBING its cached tools, so
	// flows keep their ports. Only a live session counts as connected, or this
	// chip would report a broken server as working.
	connected := registered && st.OfflineReason == ""
	return mcpServerRow{
		Name:            s.Name,
		Label:           s.DisplayName(),
		URL:             s.URL,
		AuthKind:        string(s.AuthKind),
		AuthHeader:      s.AuthHeader,
		HasToken:        s.HasAuth(),
		Enabled:         s.Enabled,
		Connected:       connected,
		ToolIDs:         st.ToolIDs,
		Instructions:    st.Instructions,
		ProtocolVersion: st.ProtocolVersion,
		ToolCount:       s.ToolCount,
		LastError:       s.LastError,
		LastConnected:   s.LastConnected,
		CreatedBy:       s.CreatedBy,
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
	}
}

// liveMCPServers indexes what this process currently has registered for the
// tenant, by server name.
//
// Only the tenant's OWN servers: an operator's instance-wide server is not
// something an org configured and must not appear on a page whose every other
// control would edit or delete it.
func (h *HTTPGateway) liveMCPServers(tenant string) map[string]mcp.ServerStatus {
	out := map[string]mcp.ServerStatus{}
	for _, st := range h.MCPServers.Catalog.ServersFor(tenant) {
		if st.Tenant != tenant {
			continue
		}
		out[st.Name] = st
	}
	return out
}

func (h *HTTPGateway) listMCPServers(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.mcpServersConfigured(rw) {
		return
	}
	rows, err := h.MCPServers.List(r.Context(), p.Tenant)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	live := h.liveMCPServers(p.Tenant)
	out := make([]mcpServerRow, 0, len(rows))
	for _, s := range rows {
		out = append(out, h.mcpRowFor(s, live))
	}
	writeJSON(rw, http.StatusOK, map[string]any{"servers": out})
}

// saveMCPServer handles both create (POST) and edit (PUT).
//
// One handler because the operation is the same one: a server is identified by
// its name, and saving under an existing name replaces that configuration and
// reconnects. Splitting them would mean two paths that must agree on
// validation, sealing, and reconnection.
func (h *HTTPGateway) saveMCPServer(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.mcpServersConfigured(rw) {
		return
	}
	var req mcpServerRequest
	if err := decodeMCPBody(r, &req); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "malformed request body")
		return
	}
	// On PUT the name is in the path and the body's copy is ignored, so a
	// mismatched body cannot rename (and thereby re-key) a server behind the
	// caller's back. The LABEL is free to change on the same request: nothing
	// references it, so renaming what a human sees costs nothing.
	if pathName := r.PathValue("name"); pathName != "" {
		req.Name = pathName
	}
	in := MCPServerInput{
		Label:      req.Label,
		Name:       req.Name,
		URL:        req.URL,
		AuthKind:   MCPAuthKind(req.AuthKind),
		AuthHeader: req.AuthHeader,
		Token:      req.Token,
		Enabled:    true,
	}
	if req.Enabled != nil {
		in.Enabled = *req.Enabled
	}

	saved, err := h.MCPServers.Save(r.Context(), p.Tenant, p.Subject, in)
	if err != nil {
		if errors.Is(err, ErrMCPServersUnconfigured) {
			writeJSONError(rw, http.StatusNotImplemented, err.Error())
			return
		}
		// Everything Save rejects is the caller's input, named and explained,
		// so it belongs in a 400 the form can display next to the field.
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	// Audited with the URL, not the token: an admin pointing the daemon at a
	// new endpoint is exactly the act a reviewer needs to see later.
	h.audit(r.Context(), p, "mcp_server.save", saved.Name, saved.URL)
	writeJSON(rw, http.StatusOK, h.mcpRowFor(saved, h.liveMCPServers(p.Tenant)))
}

// mcpServerUsage answers "what breaks if I delete this".
//
// Its own endpoint rather than a field on the row: it loads every graph in the
// org, which is fine once, when an admin opens a delete confirmation, and
// wasteful on every render of the list.
func (h *HTTPGateway) mcpServerUsage(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.mcpServersConfigured(rw) {
		return
	}
	name := r.PathValue("name")
	// Report usage only for a server that exists, so a typo'd name cannot be
	// answered with a confident "nothing uses this".
	if _, err := h.MCPServers.Store.Get(r.Context(), p.Tenant, name); err != nil {
		if errors.Is(err, ErrMCPServerNotFound) {
			writeJSONError(rw, http.StatusNotFound, "no MCP server named "+name)
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if h.svc == nil {
		writeJSONError(rw, http.StatusNotImplemented, "flow storage is not configured on this deployment")
		return
	}
	usage, err := h.svc.FlowsUsingMCPServer(r.Context(), p, p.Tenant, name)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, usage)
}

func (h *HTTPGateway) refreshMCPServer(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.mcpServersConfigured(rw) {
		return
	}
	name := r.PathValue("name")
	saved, err := h.MCPServers.Refresh(r.Context(), p.Tenant, name)
	if err != nil {
		if errors.Is(err, ErrMCPServerNotFound) {
			writeJSONError(rw, http.StatusNotFound, "no MCP server named "+name)
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, h.mcpRowFor(saved, h.liveMCPServers(p.Tenant)))
}

func (h *HTTPGateway) deleteMCPServer(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.mcpServersConfigured(rw) {
		return
	}
	name := r.PathValue("name")
	if err := h.MCPServers.Delete(r.Context(), p.Tenant, name); err != nil {
		if errors.Is(err, ErrMCPServerNotFound) {
			writeJSONError(rw, http.StatusNotFound, "no MCP server named "+name)
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	// Audited with what it broke, not just what was removed: "deleted vendor"
	// and "deleted vendor, which 4 flows were using" are different events to
	// whoever reads this back.
	detail := ""
	if h.svc != nil {
		if usage, uerr := h.svc.FlowsUsingMCPServer(r.Context(), p, p.Tenant, name); uerr == nil && usage.InUse() {
			detail = fmt.Sprintf("in use by %d flow(s)", len(usage.Flows)+usage.Hidden)
		}
	}
	h.audit(r.Context(), p, "mcp_server.delete", name, detail)
	writeJSON(rw, http.StatusOK, map[string]any{"deleted": name})
}
