// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package ticketmaster

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/drops/internal/dropstest"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

func TestTMGet_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		_, _, err := tmGet(context.Background(), "http://127.0.0.1:9/discovery/v2/events.json", 2000)
		return err
	})
}
