// SPDX-FileCopyrightText: 2026 Angels' Ware
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
//
// It also disables outbound pacing, which is process-wide state that these
// tests were leaking into each other. A stubbed 429/503 with no Retry-After
// header parks its host on fallbackCooldown (5s), and the next test to reach
// the same 127.0.0.1 key waits it out: TestSiteCheck_FiresOnTransitionsOnly
// spent 10s doing nothing but serving out two of those. The limiter's own
// behaviour is unaffected — every test in ratelimit_test.go drives an isolated
// newTestLimiter rather than this global.
func TestMain(m *testing.M) {
	SetAllowPrivateEgress(true)
	SetEgressRateLimit(0, 0, 0)
	os.Exit(m.Run())
}
