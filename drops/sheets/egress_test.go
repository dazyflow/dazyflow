// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sheets

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/drops/internal/dropstest"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

func TestGoogleDo_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		_, _, err := googleDo(context.Background(), "GET", "http://127.0.0.1:9/v4/spreadsheets/x", "tok", "", nil, 2000)
		return err
	})
}
