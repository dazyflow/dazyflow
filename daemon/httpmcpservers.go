// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
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
	Name     string `json:"name"`
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
	// Connected is the live fact: this row is registered in THIS process's
	// catalog right now. Distinct from last_connected, which is the last time
	// any replica managed it.
	Connected bool `json:"connected"`
	// ToolIDs are the step ids this server contributes, so the page can show
	// what was actually gained rather than only a count.
	ToolIDs       []string  `json:"tool_ids,omitempty"`
	ToolCount     int       `json:"tool_count"`
	LastError     string    `json:"last_error,omitempty"`
	LastConnected time.Time `json:"last_connected,omitempty"`
	CreatedBy     string    `json:"created_by,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type mcpServerRequest struct {
	Name       string `json:"name"`
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
func (h *HTTPGateway) mcpRowFor(s MCPServer, live map[string][]string) mcpServerRow {
	ids, connected := live[s.Name]
	return mcpServerRow{
		Name:          s.Name,
		URL:           s.URL,
		AuthKind:      string(s.AuthKind),
		AuthHeader:    s.AuthHeader,
		HasToken:      s.HasAuth(),
		Enabled:       s.Enabled,
		Connected:     connected,
		ToolIDs:       ids,
		ToolCount:     s.ToolCount,
		LastError:     s.LastError,
		LastConnected: s.LastConnected,
		CreatedBy:     s.CreatedBy,
		CreatedAt:     s.CreatedAt,
		UpdatedAt:     s.UpdatedAt,
	}
}

// liveMCPServers indexes what this process currently has registered for the
// tenant, by server name.
//
// Only the tenant's OWN servers: an operator's instance-wide server is not
// something an org configured and must not appear on a page whose every other
// control would edit or delete it.
func (h *HTTPGateway) liveMCPServers(tenant string) map[string][]string {
	out := map[string][]string{}
	for _, st := range h.MCPServers.Catalog.ServersFor(tenant) {
		if st.Tenant != tenant {
			continue
		}
		out[st.Name] = st.ToolIDs
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
	// caller's back.
	if pathName := r.PathValue("name"); pathName != "" {
		req.Name = pathName
	}
	in := MCPServerInput{
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
	h.audit(r.Context(), p, "mcp_server.delete", name, "")
	writeJSON(rw, http.StatusOK, map[string]any{"deleted": name})
}
