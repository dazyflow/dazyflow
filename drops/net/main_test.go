// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	SetAllowPrivateEgress(true)
	SetEgressRateLimit(0, 0, 0)
	os.Exit(m.Run())
}

// setPrivateEgress sets the package-global private-egress opt-in for the
// duration of t and restores the previous value afterwards, so tests don't
// leak state into whichever test runs next.
func setPrivateEgress(t *testing.T, v bool) {
	t.Helper()
	prev := PrivateEgressAllowed()
	SetAllowPrivateEgress(v)
	t.Cleanup(func() { SetAllowPrivateEgress(prev) })
}
