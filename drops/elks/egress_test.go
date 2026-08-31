// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package elks

import (
	"context"
	"net/http"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/dropstest"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

func TestElksDo_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		job := core.Job{ID: "j", Params: map[string]any{"api_username": "u1", "api_password": "p1"}}
		_, _, err := elksDo(context.Background(), job, http.MethodPost, "http://127.0.0.1:9/a1/sms", "from=Acme&to=%2B46700000000&message=x")
		return err
	})
}
