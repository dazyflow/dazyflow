// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"os"
	"testing"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// Allows private egress for the whole daemon test package: many tests dial fake
// servers on loopback, which the shared SSRF guard refuses by default. A test
// that needs to assert the guard BLOCKS a private address should flip it off and
// restore it with `defer hfnet.SetAllowPrivateEgress(true)`.
//
// Parallelism policy: almost every test here calls t.Parallel(), and the suite
// runs about twice as fast for it. Go releases parallel tests only after every
// sequential test has finished, so a test that mutates process-global state is
// safe as long as it does NOT call t.Parallel(). These stay sequential:
//
//   - anything calling t.Setenv or flipping a package-level var: the
//     trusted-proxy cache, the OpenTelemetry provider, the Google token
//     endpoints, the failure-email window, the Vault clock
//   - anything flipping the egress guard set below or the self origin, or
//     registering a drop, or the Google-form field fetcher
//   - the Postgres-gated tests, which TRUNCATE shared tables and would erase
//     each other's rows
func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}
