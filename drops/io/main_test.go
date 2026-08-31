// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package io

import (
	"os"
	"testing"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// TestMain enables the operator private-egress opt-in for the whole test
// process. http_download/http_upload tests stream to/from httptest servers
// bound to 127.0.0.1 via the allow_private_networks param, which is now
// gated on the operator opt-in too. Tests that omit the param stay blocked.
func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}
