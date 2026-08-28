// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package nshift

import (
	"context"
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/dropstest"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

func TestNshiftDo_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		job := core.Job{ID: "j", Params: map[string]any{"api_key": "k1", "base_url": "http://127.0.0.1:9"}}
		_, _, _, err := nshiftDo(context.Background(), job, http.MethodGet, "http://127.0.0.1:9/rs-extapi/v1/shipments/1", nil)
		return err
	})
}
