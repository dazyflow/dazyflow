// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package ticketmaster

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/dazyflow/dazyflow/engine"
)

// Ticketmaster connection verification, registered so the Apps page can test
// the API key before storing it. The label must match the drops'
// Manifest.Integration.
func init() {
	engine.RegisterConnectionVerifier("Ticketmaster", verifyTicketmaster)
}

// verifyTicketmaster confirms the key is accepted by asking for a single
// event — the same endpoint both drops use, so a key that passes here works at
// run time. The dial goes through the same SSRF-guarded client.
func verifyTicketmaster(ctx context.Context, conn map[string]string) error {
	key := strings.TrimSpace(conn["api_key"])
	if key == "" {
		return errors.New("enter your Ticketmaster API key")
	}
	q := url.Values{}
	q.Set("size", "1")
	q.Set("apikey", key)

	status, body, err := tmGet(ctx, httpBase.Get()+"/events.json?"+q.Encode(), 10000)
	if err != nil {
		// The error can carry the request URL, and the URL carries the key.
		return errors.New("could not reach Ticketmaster")
	}
	switch {
	case status == 401 || status == 403:
		if msg := extractTMError(body); msg != "" {
			return errors.New("Ticketmaster rejected the key: " + msg)
		}
		return errors.New("Ticketmaster rejected the key — check the Consumer Key of your app at developer.ticketmaster.com")
	case status == 429:
		return errors.New("Ticketmaster is rate limiting this key right now — the key looks valid, so try the test again in a minute")
	case status < 200 || status >= 300:
		return fmt.Errorf("Ticketmaster returned HTTP %d: %s", status, extractTMError(body))
	}
	return nil
}
