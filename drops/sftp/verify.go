// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sftp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/sftputil"
)

func init() {
	engine.RegisterConnectionVerifier(integration, verifySFTP)
}

const verifyTimeout = 20 * time.Second

// verifySFTP connects, authenticates, and stats the configured folder, then
// hangs up without transferring anything (sftputil.Verify).
//
// This button carries more weight here than on the other integrations,
// because host-key verification has no default: with no fingerprint or
// known_hosts configured, the connection deliberately fails — and the failure
// quotes the server's actual fingerprint. So the intended first run of "Test
// connection" is one that fails usefully, handing the operator the value to
// paste. Worth knowing before reading the error as a bug.
func verifySFTP(ctx context.Context, conn map[string]string) error {
	cfg, err := sftputil.ConfigFromConn(conn)
	if err != nil {
		return err
	}

	if err := hfnet.CheckDialHost(cfg.Addr()); err != nil {
		if strings.Contains(err.Error(), "cannot resolve") {
			return fmt.Errorf("couldn't find a server at %q — check the address", cfg.Host)
		}
		return errors.New("that looks like a local/private address — the operator must enable private-network access (DAZYFLOW_ALLOW_PRIVATE_EGRESS) to reach it")
	}

	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	return sftputil.Verify(ctx, cfg)
}
