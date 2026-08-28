// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"os"
	"testing"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// TestMain allows private egress for the whole daemon test package. Many tests
// dial fake servers on loopback (BYO secret managers, mailers, failure-notify
// webhooks) which the shared SSRF guard refuses by default. Mirrors
// drops/db/main_test.go. A test that needs to assert the guard BLOCKS a private
// address should flip it off and restore it with `defer
// hfnet.SetAllowPrivateEgress(true)` so later tests keep the package default.
func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}
