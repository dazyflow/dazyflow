// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package journey

import (
	"os"
	"testing"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}
