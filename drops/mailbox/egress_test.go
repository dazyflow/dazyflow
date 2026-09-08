// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailbox

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/drops/internal/dropstest"
	"github.com/dazyflow/dazyflow/internal/imaputil"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

// The assertion every connector owes: with the operator opt-in off, a mail
// server pointing at a loopback/private address must be refused. Without it a
// tenant could point the Mailbox integration at cloud metadata or an internal
// host — and, because a dial is followed by LOGIN, hand it the mailbox
// credentials.
func TestIMAPDial_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		_, err := imaputil.Dial(context.Background(), imaputil.Config{
			Host: "127.0.0.1", Port: 9, TLS: imaputil.ModeNone,
			Username: "u", Password: "p", Folder: "INBOX",
		})
		return err
	})
}
