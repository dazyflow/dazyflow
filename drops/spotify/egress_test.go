// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package spotify

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/drops/internal/dropstest"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

func TestSpotifyDo_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		_, _, err := spotifyDo(context.Background(), "GET", "http://127.0.0.1:9/v1/me/following", "tok", nil, 2000)
		return err
	})
}
