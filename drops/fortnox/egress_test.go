// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package fortnox

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/drops/internal/dropstest"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

func TestFortnoxDo_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		_, _, err := fortnoxDo(context.Background(), "GET", "http://127.0.0.1:9/3/customers", "tok", nil, 2000)
		return err
	})
}
