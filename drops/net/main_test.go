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
