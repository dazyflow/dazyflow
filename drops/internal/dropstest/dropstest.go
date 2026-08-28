// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dropstest holds the test scaffolding every HTTP connector carried as
// a copy-pasted egress_test.go: the private-egress opt-in each suite needs, and
// the SSRF assertion each one makes against its own `xDo`.
//
// It lives under drops/internal/ for the same reason apibase does — only
// sibling connector packages have any business calling it. It is a non-test
// package (like engine/mcp/mcptest) because the callers are _test.go files in
// OTHER packages, which cannot import another package's test binary.
//
// One thing it deliberately does NOT serve: drops/net's own egress_test.go.
// That file is an internal test of the guard itself (package net), so importing
// this package — which imports net — would be a test-time import cycle. It is
// also not a copy of anything: it tests the guard, not a connector's use of it.
package dropstest

import (
	"os"
	"testing"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// EgressTestMain runs a connector package's tests with private-network egress
// allowed, and is meant to BE that package's TestMain:
//
//	func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }
//
// The connectors dial through net.SafeHTTPClient, whose SSRF guard blocks
// loopback unless the operator opts in; the suites point each connector at a
// 127.0.0.1 httptest server, so they need the same opt-in production gets via
// DAZYFLOW_ALLOW_PRIVATE_EGRESS. It calls os.Exit, exactly as a hand-written
// TestMain would.
func EgressTestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

// AssertSSRFBlocked turns the operator opt-in off for the duration of call and
// requires the dial guard to refuse it.
//
// This is the assertion every connector owes: with the opt-in off, a base_url
// pointing at a loopback/private address must be refused — otherwise a tenant
// could exfiltrate that connector's credential to cloud metadata or an internal
// host. Each connector passes a closure because their `xDo` signatures differ
// in arity and in what they take (some a core.Job, some a bare token); the
// error is the only part the assertion cares about.
//
// The opt-in is process-global, so a caller must not run this in parallel with
// tests that need egress allowed. That is why it restores on defer rather than
// via t.Cleanup: the window closes at the end of the call, not at the end of
// the test.
func AssertSSRFBlocked(t *testing.T, call func() error) {
	t.Helper()
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	if err := call(); err == nil || !hfnet.IsSSRFError(err) {
		t.Fatalf("want ssrf_blocked, got %v", err)
	}
}
