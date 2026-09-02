// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"os"
	"testing"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// TestMain allows private egress for the whole daemon test package. Many tests
// dial fake servers on loopback (BYO secret managers, mailers, failure-notify
// webhooks) which the shared SSRF guard refuses by default. Mirrors
// drops/db/main_test.go. A test that needs to assert the guard BLOCKS a private
// address should flip it off and restore it with `defer
// hfnet.SetAllowPrivateEgress(true)` so later tests keep the package default.
// Parallelism policy for this package.
//
// Almost every test here calls t.Parallel(); the suite runs about twice as fast
// for it. Go releases parallel tests only after every sequential test has
// finished, so a test that mutates process-global state is safe as long as it
// does NOT call t.Parallel() — and that is the rule. These stay sequential:
//
//   - anything calling t.Setenv, or flipping a package-level var: the trusted-
//     proxy cache (ratelimit), the OpenTelemetry provider (tracing), the Google
//     token endpoints (google_exchange, google_signin), the failure-email window
//     (failure_notify_manual), the Vault clock (vault_secrets)
//   - anything flipping the egress guard set below (failure_notify, mcpservers,
//     webapis) or the self origin (failure_notify_loop), or registering a drop
//     (httpconnectionverify), or the Google-form field fetcher (rowsource)
//   - the Postgres-gated tests, which TRUNCATE shared tables and would erase
//     each other's rows: billing, encrypted_secrets, eventbus_pg, gitmirror_pg,
//     leader, pgstores, runlog, runner_stores, support_prod, usage
//
// Adding a test that touches any of those? Leave t.Parallel() out.
func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}
