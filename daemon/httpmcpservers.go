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

type mcpAPI struct {
	auditor
	svc        *Service
	MCPServers *MCPServers
}

func (h *HTTPGateway) mcpAPI() *mcpAPI {
	return &mcpAPI{auditor: h.auditor(), svc: h.svc, MCPServers: h.MCPServers}
}

type mcpServerRow struct {
	Name string `json:"name"`
	// Always populated on the wire, falling back to the id for an older row.
	Label      string `json:"label"`
	URL        string `json:"url"`
	AuthKind   string `json:"auth_kind"`
	AuthHeader string `json:"auth_header,omitempty"`
	// Whether a credential is stored; the value itself never comes back.
	HasToken bool `json:"has_token"`
	Enabled  bool `json:"enabled"`
	// The live fact for THIS process, not a stored column.
	Connected bool     `json:"connected"`
	ToolIDs   []string `json:"tool_ids,omitempty"`
	// Verbatim third-party prose: shown to an admin, never acted on.
	Instructions    string    `json:"instructions,omitempty"`
	ProtocolVersion string    `json:"protocol_version,omitempty"`
	ToolCount       int       `json:"tool_count"`
	LastError       string    `json:"last_error,omitempty"`
	LastConnected   time.Time `json:"last_connected,omitempty"`
	CreatedBy       string    `json:"created_by,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type mcpServerRequest struct {
	Label string `json:"label,omitempty"`
	// The UI never sends it: two clients slugging differently would be two ids.
	Name       string `json:"name,omitempty"`
	URL        string `json:"url"`
	AuthKind   string `json:"auth_kind"`
	AuthHeader string `json:"auth_header,omitempty"`
	Token      string `json:"token,omitempty"`
	Enabled    *bool  `json:"enabled,omitempty"`
}

func (h *mcpAPI) mcpServersConfigured(rw http.ResponseWriter) bool {
	if h.MCPServers == nil || h.MCPServers.Store == nil || h.MCPServers.Catalog == nil {
		writeJSONError(rw, http.StatusNotImplemented, "MCP servers are not configured on this deployment")
		return false
	}
	return true
}

func decodeMCPBody(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(v)
}

func (h *mcpAPI) mcpRowFor(s MCPServer, live map[string]mcp.ServerStatus) mcpServerRow {
	st, registered := live[s.Name]
	// Registered is not connected: an offline registration still describes its steps.
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

// What THIS process has registered, which another replica may differ on.
func (h *mcpAPI) liveMCPServers(tenant string) map[string]mcp.ServerStatus {
	out := map[string]mcp.ServerStatus{}
	for _, st := range h.MCPServers.Catalog.ServersFor(tenant) {
		if st.Tenant != tenant {
			continue
		}
		out[st.Name] = st
	}
	return out
}

func (h *mcpAPI) listMCPServers(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

func (h *mcpAPI) saveMCPServer(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.mcpServersConfigured(rw) {
		return
	}
	var req mcpServerRequest
	if err := decodeMCPBody(r, &req); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "malformed request body")
		return
	}
	// The path wins on PUT, so a mismatched body cannot rename the server.
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
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r.Context(), p, "mcp_server.save", saved.Name, saved.URL)
	writeJSON(rw, http.StatusOK, h.mcpRowFor(saved, h.liveMCPServers(p.Tenant)))
}

func (h *mcpAPI) mcpServerUsage(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.mcpServersConfigured(rw) {
		return
	}
	name := r.PathValue("name")
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

func (h *mcpAPI) refreshMCPServer(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

func (h *mcpAPI) deleteMCPServer(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	detail := ""
	if h.svc != nil {
		if usage, uerr := h.svc.FlowsUsingMCPServer(r.Context(), p, p.Tenant, name); uerr == nil && usage.InUse() {
			detail = fmt.Sprintf("in use by %d flow(s)", len(usage.Flows)+usage.Hidden)
		}
	}
	h.audit(r.Context(), p, "mcp_server.delete", name, detail)
	writeJSON(rw, http.StatusOK, map[string]any{"deleted": name})
}
