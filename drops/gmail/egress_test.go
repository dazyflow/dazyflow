// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gmail

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/drops/internal/dropstest"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

func TestGmailDo_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		_, _, err := gmailDo(context.Background(), "GET", "http://127.0.0.1:9/gmail/v1/users/me/messages", "tok", "", nil, 2000)
		return err
	})
}
