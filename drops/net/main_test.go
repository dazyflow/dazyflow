// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"os"
	"testing"
)

// TestMain enables the operator private-egress opt-in for the whole test
// process. These tests exercise httptest servers bound to 127.0.0.1 via the
// allow_private_networks param, which is now ALSO gated on the operator
// opt-in (SetAllowPrivateEgress). Tests that deliberately omit the param
// stay blocked regardless — the guard requires param AND opt-in.
func TestMain(m *testing.M) {
	SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}
