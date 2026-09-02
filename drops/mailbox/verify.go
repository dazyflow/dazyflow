// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/imaputil"
)

// Connection verification for the Mailbox integration, registered so the Apps
// page can test credentials before storing them. The label matches the drops'
// Manifest.Integration.
func init() {
	engine.RegisterConnectionVerifier(integration, verifyMailbox)
}

// verifyTimeout bounds the probe. Shorter than a run's default: someone is
// watching a spinner, and a mail server that needs longer than this to say
// hello is a finding in itself.
const verifyTimeout = 15 * time.Second

// verifyMailbox connects to the configured IMAP server, logs in, and opens the
// folder — then hangs up without reading a message (imaputil.Verify). It
// exercises every field a run depends on, including the folder name, which is
// the one most likely to be quietly wrong: a mistyped folder fails per-run,
// deep inside a flow, where nothing points back at this page.
//
// Failures are reported in the words of the form above, not the protocol's.
func verifyMailbox(ctx context.Context, conn map[string]string) error {
	cfg, err := imaputil.ConfigFromConn(conn)
	if err != nil {
		return err
	}
	// A half-filled login would otherwise become an unauthenticated session
	// that this check happily passes, leaving someone with a green tick for
	// credentials the server was never asked to rule on — the failure
	// smtputil.Auth was made loud for.
	switch {
	case cfg.Username == "" && cfg.Password == "":
		return errors.New("enter the mailbox username and password")
	case cfg.Username == "":
		return errors.New("a password is set but no username — enter the mailbox username (usually your email address)")
	case cfg.Password == "":
		return errors.New("a username is set but no password — enter the mailbox password, or an app password if your provider issues them")
	}

	// CheckDialHost fails for two very different reasons: the host doesn't
	// resolve at all (a typo'd server name) vs. it resolves to a private/LAN
	// address (the egress guard). Don't tell someone with a typo to enable
	// private-network access — say the address looks wrong. Checked here as
	// well as inside Dial so the message can be about this form.
	if err := hfnet.CheckDialHost(cfg.Addr()); err != nil {
		if strings.Contains(err.Error(), "cannot resolve") {
			return fmt.Errorf("couldn't find a mail server at %q — check the address", cfg.Host)
		}
		return errors.New("that looks like a local/private address — the operator must enable private-network access (DAZYFLOW_ALLOW_PRIVATE_EGRESS) to reach it")
	}

	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	if err := imaputil.Verify(ctx, cfg); err != nil {
		return fmt.Errorf("couldn't read the mailbox: %w", err)
	}
	return nil
}
