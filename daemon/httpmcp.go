// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core/buildinfo"
	"github.com/dazyflow/dazyflow/mcp/server"
)

// mcpEndpointAPI serves dzd's own MCP tools over streamable HTTP at POST /mcp, so
// a remote client — claude.ai, the Claude apps, Claude Code with
// --transport http — reaches them without running dz-mcp. Stateless: one
// JSON-RPC message in, one JSON reply out, no SSE stream, no Mcp-Session-Id.
//
// The tools are mcp/server's, unchanged. Their DazydClient is pointed at this
// gateway's own routes in-process and carries the caller's credential, so
// authorization, validation and FlowUpdated events run exactly as they do for
// any other API call — there is no second copy of the policy to drift.
type mcpEndpointAPI struct {
	urlBuilder
	svc           *Service
	logger        *log.Logger
	originAllowed func(origin string) bool
}

func (h *HTTPGateway) mcpEndpointAPI() *mcpEndpointAPI {
	return &mcpEndpointAPI{urlBuilder: h.urls(), svc: h.svc, logger: h.logger, originAllowed: h.originAllowed}
}

func (h *mcpEndpointAPI) handler(inner http.Handler) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		// DNS-rebinding defense the transport spec requires. A remote
		// connector calls server-to-server and sends no Origin at all.
		if origin := r.Header.Get("Origin"); origin != "" && !h.originAllowed(origin) {
			writeAPIError(rw, http.StatusForbidden, "mcp_origin",
				fmt.Sprintf("MCP request from disallowed origin %q", origin))
			return
		}
		if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !server.SupportsProtocolVersion(v) {
			writeAPIError(rw, http.StatusBadRequest, "mcp_protocol_version",
				fmt.Sprintf("unsupported MCP-Protocol-Version %q", v))
			return
		}
		// Bearer only: a session cookie is ambient, and nothing a browser
		// sends on its own should be able to drive the tools.
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if token == "" || token == r.Header.Get("Authorization") {
			h.unauthorized(rw, r, "")
			return
		}
		p, err := h.svc.Authenticate(r.Context(), token)
		if err != nil {
			if errors.Is(err, auth.ErrAccountSuspended) {
				writeJSONError(rw, http.StatusForbidden, "account suspended")
				return
			}
			h.unauthorized(rw, r, "invalid_token")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, server.MaxMessageBytes))
		if err != nil {
			writeAPIError(rw, http.StatusRequestEntityTooLarge, "payload_too_large",
				fmt.Sprintf("MCP message exceeds %d bytes", server.MaxMessageBytes))
			return
		}

		base := h.effectiveBaseURL(r)
		if base == "" {
			base = "http://dzd.invalid"
		}
		client := server.NewDazydClient(base, token)
		client.HTTP.Transport = inProcessTransport{handler: inner, outer: r}
		srv := &server.Server{Name: "dazyflow", Version: buildinfo.Version, Logger: h.logger, Instructions: server.Instructions}
		for _, t := range server.BuildTools(client, server.Defaults{Tenant: p.Tenant, Workspace: p.Workspace}) {
			srv.Register(t)
		}
		reply := srv.HandleMessage(r.Context(), body)
		if reply == nil {
			rw.WriteHeader(http.StatusAccepted)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		if _, err := rw.Write(reply); err != nil {
			h.logger.Printf("mcp: write response: %v", err)
		}
	}
}

// unauthorized answers 401 with the challenge an MCP client follows to find
// the authorization server (RFC 9728 §5.1).
func (h *mcpEndpointAPI) unauthorized(rw http.ResponseWriter, r *http.Request, errCode string) {
	challenge := `Bearer realm="dazyflow-mcp"`
	if errCode != "" {
		challenge += fmt.Sprintf(`, error=%q`, errCode)
	}
	if base := h.effectiveBaseURL(r); base != "" {
		challenge += fmt.Sprintf(`, resource_metadata=%q`, base+"/.well-known/oauth-protected-resource/mcp")
	}
	rw.Header().Set("WWW-Authenticate", challenge)
	writeJSONError(rw, http.StatusUnauthorized, "missing or invalid Authorization: Bearer <token>")
}

// inProcessTransport serves a DazydClient request with the gateway's own
// handler instead of the network. The outer request's host and forwarding
// headers are carried over so handlers that build absolute URLs (connection
// authorize links, webhook URLs) build the ones the caller can reach.
type inProcessTransport struct {
	handler http.Handler
	outer   *http.Request
}

func (t inProcessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Host = t.outer.Host
	req.RemoteAddr = t.outer.RemoteAddr
	req.TLS = t.outer.TLS
	req.RequestURI = req.URL.RequestURI()
	// The server never hands a handler a nil Body; a body-less client
	// request has one, and handlers that decode unconditionally would panic.
	if req.Body == nil {
		req.Body = http.NoBody
	}
	for _, k := range []string{"X-Forwarded-Proto", "X-Forwarded-Host", "X-Forwarded-For", "X-Real-Ip"} {
		if v := t.outer.Header.Get(k); v != "" {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	t.handler.ServeHTTP(rec, req)
	return rec.Result(), nil
}
