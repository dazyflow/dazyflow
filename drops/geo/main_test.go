// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package geo

import (
	"os"
	"testing"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// TestMain disables outbound pacing for the whole test process. Every geocoder
// call goes through hfnet.Do and so through the process-wide egress limiter,
// and the failure-path tests stub 403 and 429 responses with no Retry-After
// header — which parks the stub host on fallbackCooldown (5s). The next test
// to hit 127.0.0.1 then waited it out for no reason: TestPhoton_ForwardSuccess
// and TestGeoHTTPFailure_StatusCodes were 5s each, entirely spent asleep on a
// cooldown another test had set. Pacing a stub server tests nothing.
func TestMain(m *testing.M) {
	hfnet.SetEgressRateLimit(0, 0, 0)
	os.Exit(m.Run())
}
