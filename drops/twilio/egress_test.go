// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package twilio

import (
	"context"
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/dropstest"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

func TestTwilioDo_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		job := core.Job{ID: "j", Params: map[string]any{"account_sid": "ACx", "auth_token": "tok"}}
		_, _, err := twilioDo(context.Background(), job, http.MethodPost, "http://127.0.0.1:9/Accounts/ACx/Messages.json", "To=%2B1&Body=x&From=%2B2")
		return err
	})
}
