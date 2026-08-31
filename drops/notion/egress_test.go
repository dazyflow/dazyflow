// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package notion

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/drops/internal/dropstest"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

func TestNotionDo_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		_, _, err := notionDo(context.Background(), "POST", "http://127.0.0.1:9/v1/databases/x/query", "tok", []byte("{}"), 2000)
		return err
	})
}
