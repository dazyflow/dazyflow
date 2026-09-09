// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dropstest holds the test scaffolding every HTTP connector carried as
// a copy-pasted egress_test.go: the private-egress opt-in each suite needs, and
// the SSRF assertion each one makes against its own `xDo`.
//
// It is a non-test package (like engine/mcp/mcptest) because the callers are
// _test.go files in OTHER packages, which cannot import another package's test
// binary. drops/net's own egress_test.go stays outside it: it is an internal
// test of the guard itself, so importing this would be a test-time cycle.
package dropstest

import (
	"os"
	"testing"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// EgressTestMain runs a connector package's tests with private-network egress
// allowed, and is meant to BE that package's TestMain:
//
//	func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }
//
// The suites point each connector at a 127.0.0.1 httptest server, which
// net.SafeHTTPClient's SSRF guard blocks without the same opt-in production
// gets via DAZYFLOW_ALLOW_PRIVATE_EGRESS. It calls os.Exit, as a TestMain does.
func EgressTestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

// AssertSSRFBlocked turns the operator opt-in off for the duration of call and
// requires the dial guard to refuse it — the assertion every connector owes,
// since otherwise a tenant could point base_url at cloud metadata and exfiltrate
// that connector's credential. Each connector passes a closure because their
// `xDo` signatures differ; the error is the only part that matters.
//
// The opt-in is process-global, so this must not run in parallel with tests that
// need egress allowed — hence restoring on defer rather than via t.Cleanup.
func AssertSSRFBlocked(t *testing.T, call func() error) {
	t.Helper()
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	if err := call(); err == nil || !hfnet.IsSSRFError(err) {
		t.Fatalf("want ssrf_blocked, got %v", err)
	}
}
