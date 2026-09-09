// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package webapi

import (
	"context"
	"net/http"
	"sync/atomic"
)

// Doer performs one guarded outbound HTTP call. The signature is drops/net.Do's,
// exactly, so cmd/dzd can wire that function in as-is.
//
// INJECTED rather than imported, because drops/net imports engine, so importing
// it here would be a cycle — the same hook pattern engine/mcp already uses for
// its SSRF dial guard.
//
// What the daemon's Do brings, and what a hand-rolled http.Client here would
// silently drop: the SSRF dial guard, the per-tenant egress allowlist, the
// per-(tenant, host) rate limit and 429 cooldown, and a response cap.
type Doer func(ctx context.Context, method, url string, headers map[string]string, body []byte, timeoutMS, maxBytes int) (int, []byte, http.Header, error)

var doerHook atomic.Pointer[Doer]

// SetDoer installs the guarded HTTP caller. Passing nil clears it.
//
// Unset means no web-API step can run — deliberately. A default that fell back
// to http.DefaultClient would make every guard above opt-in, and the failure
// would be invisible: calls would work, just unguarded. Failing loudly is the
// only safe default. Tests inject a fake; cmd/dzd wires hfnet.Do.
func SetDoer(d Doer) {
	if d == nil {
		doerHook.Store(nil)
		return
	}
	doerHook.Store(&d)
}

func currentDoer() (Doer, bool) {
	if p := doerHook.Load(); p != nil && *p != nil {
		return *p, true
	}
	return nil, false
}
