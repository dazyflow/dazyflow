// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package discord

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/dropstest"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

func TestDiscordDo_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		job := core.Job{ID: "j", Params: map[string]any{}}
		_, _, err := discordDo(context.Background(), job, "http://127.0.0.1:9/api/webhooks/1/tok", []byte(`{"content":"x"}`))
		return err
	})
}
