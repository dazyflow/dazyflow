// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package roaring

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/dropstest"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

// Roaring's guarded call is the token exchange itself, not a data endpoint:
// resolveToken posts the client credentials to base_url, so that is where a
// private address has to be refused.
func TestResolveToken_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		job := core.Job{ID: "j", Params: map[string]any{
			"client_key": "k1", "client_secret": "s1", "base_url": "http://127.0.0.1:9",
		}}
		_, err := resolveToken(context.Background(), job)
		return err
	})
}
