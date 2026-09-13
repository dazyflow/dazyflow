// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/dazyflow/dazyflow/core"
)

type limitsAPI struct {
	svc *Service
}

func (h *HTTPGateway) limitsAPI() *limitsAPI {
	return &limitsAPI{svc: h.svc}
}

// maxRequestBody is the global ceiling on any request body. It equals the
// largest legitimate payload the API accepts (a file upload, maxUploadBytes);
// handlers that decode smaller bodies wrap r.Body in a stricter
// http.MaxBytesReader of their own (e.g. 64 KiB for secret values), which
// still trips first. This ceiling is just the backstop.
const maxRequestBody = maxUploadBytes

// limitRequestBody guards body-carrying requests two ways. It rejects a
// request whose declared Content-Length already exceeds the ceiling *before*
// any body is read, so an oversized request can't force a large allocation
// before a per-route MaxBytesReader would trip. And it wraps r.Body in
// http.MaxBytesReader as a backstop — covering chunked requests (Content-Length
// == -1, so the length pre-check can't see them) and any route that forgets to
// set its own limit. Per-route handlers may re-wrap r.Body with a smaller
// limit; the stricter one trips first, so this never loosens an existing cap.
func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			if r.ContentLength > maxRequestBody {
				writeAPIError(rw, http.StatusRequestEntityTooLarge, "payload_too_large",
					fmt.Sprintf("request body exceeds %d bytes", maxRequestBody))
				return
			}
			r.Body = http.MaxBytesReader(rw, r.Body, maxRequestBody)
		}
		next.ServeHTTP(rw, r)
	})
}

// workspaceLimits serves GET /api/v1/admin/limits — a read-only view of
// the effective limits that apply to the caller's tenant: the per-tenant
// disk quota (used + limit) plus the daemon-wide graph caps. organization:admin
// only. There's no write side — these are operator-configured (flags), so
// the admin UI surfaces them rather than pretending to edit them.
func (h *limitsAPI) workspaceLimits(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireOrgAdmin(rw, p) {
		return
	}
	out := map[string]any{
		"tenant":                    p.Tenant,
		"max_graph_nodes":           h.svc.MaxGraphNodes,          // 0 = unlimited
		"max_graph_edges":           h.svc.MaxGraphEdges,          // 0 = unlimited
		"max_graph_timeout_seconds": h.svc.MaxGraphTimeoutSeconds, // 0 = no ceiling
	}
	// Per-tenant disk quota, when a quota provider is wired.
	if h.svc.Engine != nil && h.svc.Engine.Quota != nil {
		q := map[string]any{"limit_bytes": h.svc.Engine.Quota.Limit(p.Tenant)}
		if used, err := h.svc.Engine.Quota.Used(p.Tenant); err == nil {
			q["used_bytes"] = used
		}
		out["quota"] = q
	}
	writeJSON(rw, http.StatusOK, out)
}

// defaultMaxConnections is the ceiling on simultaneously accepted connections.
// Generous on purpose: it is a backstop against socket and goroutine growth,
// not a capacity plan. Each idle connection costs a goroutine and a read
// buffer, so a thousand is a few tens of megabytes — and a browser holds
// several open at once under keep-alive, so a tight cap would lock out real
// users long before it inconvenienced anyone else.
const defaultMaxConnections = 1024

// limitListener stops accepting once maxConns are open, so excess connections
// wait in the kernel's backlog and time out there rather than each buying a
// goroutine and a buffer inside the process.
//
// Written here rather than pulled from x/net/netutil, which is the same thirty
// lines: this package already keeps its own token bucket for the same reason.
//
// The trade to know about: when the cap is reached NOTHING is accepted, health
// checks included, so a daemon at its ceiling looks dead to an orchestrator
// rather than busy. That is why the default is high enough that reaching it
// means something is wrong rather than merely popular.
type limitListener struct {
	net.Listener
	sem chan struct{}
}

func newLimitListener(ln net.Listener, maxConns int) net.Listener {
	if maxConns <= 0 {
		return ln
	}
	return &limitListener{Listener: ln, sem: make(chan struct{}, maxConns)}
}

func (l *limitListener) Accept() (net.Conn, error) {
	l.sem <- struct{}{}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	// OnceFunc because a net.Conn may be closed more than once — by the
	// server's own teardown and by a handler's defer — and a slot released
	// twice would let the cap drift upwards for the life of the process.
	return &limitConn{Conn: conn, release: sync.OnceFunc(func() { <-l.sem })}, nil
}

type limitConn struct {
	net.Conn
	release func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.release()
	return err
}
