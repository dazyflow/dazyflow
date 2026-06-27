// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"os"
	"testing"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// TestMain enables the operator private-egress opt-in for the whole test
// process. The Postgres/MySQL integration tests (gated on DAZYFLOW_TEST_DB /
// DAZYFLOW_TEST_MYSQL) connect to a trusted local test database on
// 127.0.0.1, which the db drop's SSRF dial guard refuses by default — that
// guard exists to stop *tenant-supplied* DSNs reaching internal hosts, which
// doesn't apply to a test harness pointing at its own throwaway DB. Enabling
// it here mirrors an operator setting DAZYFLOW_ALLOW_PRIVATE_EGRESS, matching
// the convention in drops/io, drops/net, and tests/e2e.
func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}
