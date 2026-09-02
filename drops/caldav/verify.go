// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package caldav

import (
	"context"
	"time"

	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/caldavutil"
)

// Connection verification for the Calendar integration, registered so the
// Apps page can test credentials before storing them. The label matches the
// drops' Manifest.Integration.
func init() {
	engine.RegisterConnectionVerifier(integration, verifyCalDAV)
}

// verifyTimeout bounds the probe. CalDAV discovery is several round trips —
// principal, then home set, then the collections — so it gets a little more
// room than the single-request integrations.
const verifyTimeout = 25 * time.Second

// verifyCalDAV signs in, discovers the account's calendars, and confirms the
// configured one is among them, without reading a single event
// (caldavutil.Verify).
//
// Naming the calendars in the failure is the point: providers hand out URLs
// that may be a root, a principal or one calendar, and an account often holds
// several. "Ambiguous" would leave someone guessing at spellings, so the
// error lists what's actually there to copy from.
func verifyCalDAV(ctx context.Context, conn map[string]string) error {
	cfg, err := caldavutil.ConfigFromConn(conn)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	return caldavutil.Verify(ctx, cfg)
}
