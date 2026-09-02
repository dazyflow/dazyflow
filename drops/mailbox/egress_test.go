// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailbox

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/drops/internal/dropstest"
	"github.com/dazyflow/dazyflow/internal/imaputil"
)

// The suites here point the drop at a 127.0.0.1 IMAP server, so they need the
// same private-egress opt-in production gets via DAZYFLOW_ALLOW_PRIVATE_EGRESS.
//
// Nothing in this package may call t.Parallel(): both the egress opt-in and the
// cursor store are process-global, and AssertSSRFBlocked turns the opt-in off
// for the duration of its call.
func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

// TestIMAPDial_SSRFGuardBlocksPrivate is the assertion every connector owes:
// with the operator opt-in off, a mail server pointing at a loopback/private
// address must be refused. Without it a tenant could point the Mailbox
// integration at cloud metadata or an internal host — and, because a dial is
// followed by LOGIN, hand it the mailbox credentials.
func TestIMAPDial_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		_, err := imaputil.Dial(context.Background(), imaputil.Config{
			Host: "127.0.0.1", Port: 9, TLS: imaputil.ModeNone,
			Username: "u", Password: "p", Folder: "INBOX",
		})
		return err
	})
}
